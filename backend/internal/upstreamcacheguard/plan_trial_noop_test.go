package upstreamcacheguard

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// Model repository writes advancing both version and updated_at. The clock
// advances on each write, so a redundant write cannot pass by timestamp luck.
type planTrialWriteClockStore struct {
	*memoryStore
	writes int
}

type planTrialWriteClockTx struct {
	Tx
	store *planTrialWriteClockStore
}

func (s *planTrialWriteClockStore) Transact(ctx context.Context, group int64, fn func(Tx) error) error {
	return s.memoryStore.Transact(ctx, group, func(tx Tx) error {
		return fn(&planTrialWriteClockTx{Tx: tx, store: s})
	})
}

func (tx *planTrialWriteClockTx) Write(kind, stateKey string, value any) (int64, error) {
	version, err := tx.Tx.Write(kind, stateKey, value)
	if err == nil {
		tx.store.writes++
		memory := tx.Tx.(*memoryTx)
		rowKey := key(memory.group, kind, stateKey)
		row := memory.s.rows[rowKey]
		row.UpdatedAt = testNow.Add(time.Duration(tx.store.writes) * time.Second)
		memory.s.rows[rowKey] = row
	}
	return version, err
}

func TestPlanTrialExistingSessionDoesNotWrite(t *testing.T) {
	e, memory := testEngine(t)
	s := e.Settings()
	s.TrialPercent = 100
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	store := &planTrialWriteClockStore{memoryStore: memory}
	e.store = store
	r := request("natural", "same-session", testNow)
	first, err := e.PlanTrial(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	rowKey := key(r.GroupID, SessionKind, SessionKey(r))
	before := memory.rows[rowKey]
	quotaBefore := memory.rows[key(r.GroupID, GroupKind, "quota")]
	if before.Version != 1 || before.UpdatedAt.IsZero() || !first.NaturalNew || !first.TrialSlot || first.TrialOrdinal != 1 {
		t.Fatalf("first plan did not persist natural identity once: plan=%+v row=%+v", first, before)
	}
	for i := 0; i < 5; i++ {
		retry, err := e.PlanTrial(context.Background(), r)
		if err != nil || retry != first {
			t.Fatalf("retry changed plan: first=%+v retry=%+v err=%v", first, retry, err)
		}
	}
	r.ID = "later-request"
	later, err := e.PlanTrial(context.Background(), r)
	if err != nil || later.NaturalNew || later.TrialSlot || later.TrialOrdinal != first.TrialOrdinal {
		t.Fatalf("existing session received another natural slot: %+v %v", later, err)
	}
	after := memory.rows[rowKey]
	quotaAfter := memory.rows[key(r.GroupID, GroupKind, "quota")]
	if after.Version != before.Version || !after.UpdatedAt.Equal(before.UpdatedAt) || !bytes.Equal(after.Payload, before.Payload) {
		t.Fatalf("read-only plan changed durable session: before=%+v after=%+v", before, after)
	}
	if quotaAfter.Version != quotaBefore.Version || !quotaAfter.UpdatedAt.Equal(quotaBefore.UpdatedAt) || !bytes.Equal(quotaAfter.Payload, quotaBefore.Payload) || store.writes != 2 {
		t.Fatalf("read-only plan wrote quota/session: writes=%d before=%+v after=%+v", store.writes, quotaBefore, quotaAfter)
	}
}

func TestPlanTrialConcurrentRetryReservesAndWritesOnce(t *testing.T) {
	e, memory := testEngine(t)
	s := e.Settings()
	s.TrialPercent = 100
	if err := e.ApplySettings(s); err != nil {
		t.Fatal(err)
	}
	store := &planTrialWriteClockStore{memoryStore: memory}
	e.store = store
	r := request("natural", "concurrent-session", testNow)
	const count = 32
	plans := make([]TrialPlan, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			plans[i], errs[i] = e.PlanTrial(context.Background(), r)
		}(i)
	}
	wg.Wait()
	for i := range plans {
		if errs[i] != nil || !plans[i].NaturalNew || !plans[i].TrialSlot || plans[i].TrialOrdinal != 1 || plans[i] != plans[0] {
			t.Fatalf("concurrent retry %d changed plan: %+v %v", i, plans[i], errs[i])
		}
	}
	row := memory.rows[key(r.GroupID, SessionKind, SessionKey(r))]
	quotaRow := memory.rows[key(r.GroupID, GroupKind, "quota")]
	var quota GroupState
	if err := json.Unmarshal(quotaRow.Payload, &quota); err != nil {
		t.Fatal(err)
	}
	if row.Version != 1 || quotaRow.Version != 1 || quota.NaturalNewCount != 1 || store.writes != 2 {
		t.Fatalf("concurrent retry duplicated durable writes or quota: session=%+v quota=%+v state=%+v writes=%d", row, quotaRow, quota, store.writes)
	}
}
