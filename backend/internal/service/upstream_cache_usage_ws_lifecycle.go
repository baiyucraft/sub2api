package service

import (
	"context"
	"errors"
	"sync"

	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/tidwall/gjson"
)

// Each accepted response.create owns its own admission and completion. The
// connection metadata only supplies the authenticated logical session identity.
type upstreamCacheGuardWSTurns struct {
	mu      sync.Mutex
	seed    context.Context
	pending map[int]context.Context
	started int
}

func newUpstreamCacheGuardWSTurns(ctx context.Context) *upstreamCacheGuardWSTurns {
	return &upstreamCacheGuardWSTurns{seed: ctx, pending: make(map[int]context.Context)}
}

func (t *upstreamCacheGuardWSTurns) turnContext(turn int, payload []byte) context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ctx := t.pending[turn]; ctx != nil {
		return ctx
	}
	ctx := WithUpstreamCacheGuardTurn(t.seed, payload, turn == 1)
	t.pending[turn] = ctx
	if turn > t.started {
		t.started = turn
	}
	return ctx
}

func (t *upstreamCacheGuardWSTurns) begin(turn int, account *Account, payload []byte, model, protocol string) context.Context {
	ctx := t.turnContext(turn, payload)
	BeginUpstreamCacheGuardSend(ctx, account, model, protocol)
	return ctx
}

func (t *upstreamCacheGuardWSTurns) beginAdmitted(turn int, account *Account, payload []byte, model, protocol string) (context.Context, error) {
	ctx := t.turnContext(turn, payload)
	if err := BeginUpstreamCacheGuardSend(ctx, account, model, protocol); err != nil {
		t.finish(turn, nil, err)
		return ctx, err
	}
	return ctx, nil
}

func (t *upstreamCacheGuardWSTurns) finish(turn int, result *OpenAIForwardResult, err error) {
	t.mu.Lock()
	ctx := t.pending[turn]
	delete(t.pending, turn)
	t.mu.Unlock()
	if ctx != nil {
		FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, err)
	}
}

func (t *upstreamCacheGuardWSTurns) finishRemaining() {
	t.mu.Lock()
	pending := t.pending
	t.pending = make(map[int]context.Context)
	t.mu.Unlock()
	for _, ctx := range pending {
		FinishOpenAIUpstreamCacheGuardAttempt(ctx, nil, errors.New("upstream turn ended without completion"))
	}
}

// This wrapper sits inside image preparation so Begin sees the final outgoing
// model/body and runs immediately before the actual upstream frame write.
type upstreamCacheGuardWSFrameConn struct {
	inner   openaiwsv2.FrameConn
	turns   *upstreamCacheGuardWSTurns
	account *Account
	model   func() string
}

func (c *upstreamCacheGuardWSFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	typ, payload, err := c.inner.ReadFrame(ctx)
	if err == nil && (typ == coderws.MessageText || typ == coderws.MessageBinary) && openAIStreamDataStartsVisibleOutput(string(payload), "") {
		c.turns.mu.Lock()
		turnCtx := c.turns.pending[c.turns.started]
		c.turns.mu.Unlock()
		if turnCtx != nil {
			markOpenAITTFTGuardFirstToken(turnCtx)
		}
	}
	return typ, payload, err
}

func (c *upstreamCacheGuardWSFrameConn) WriteFrame(ctx context.Context, typ coderws.MessageType, payload []byte) error {
	turn := 0
	if (typ == coderws.MessageText || typ == coderws.MessageBinary) && gjson.GetBytes(payload, "type").String() == "response.create" {
		c.turns.mu.Lock()
		turn = c.turns.started + 1
		c.turns.mu.Unlock()
		model := gjson.GetBytes(payload, "model").String()
		if model == "" && c.model != nil {
			model = c.model()
		}
		if _, err := c.turns.beginAdmitted(turn, c.account, payload, model, "openai_ws"); err != nil {
			return err
		}
	}
	err := c.inner.WriteFrame(ctx, typ, payload)
	if err != nil && turn > 0 {
		c.turns.finish(turn, nil, err)
	}
	return err
}

func (c *upstreamCacheGuardWSFrameConn) Close() error { return c.inner.Close() }
