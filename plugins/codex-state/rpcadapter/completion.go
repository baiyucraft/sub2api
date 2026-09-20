package rpcadapter

import (
	"context"
	"log"
	"time"

	pluginv1 "github.com/baiyucraft/codex-state-plugin/sdk/v1"
)

const completionTimeout = 5 * time.Second

// Completion is a reverse host RPC, independent of the cancelled Forward stream
// and of runtime_active. Its failure cannot replace the business result. The
// normal End frame remains a compatible completion proof for older hosts.
func (s *Server) completeRequest(requestID string) {
	if requestID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
	defer cancel()
	client, err := s.host.connection(nil)
	if err == nil {
		_, err = client.CompleteRequest(ctx, &pluginv1.CompleteRequestRequest{RequestId: requestID})
	}
	if err != nil {
		log.Print("codex-state: request completion acknowledgement failed")
	}
}
