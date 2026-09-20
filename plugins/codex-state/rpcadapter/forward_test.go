package rpcadapter

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baiyucraft/codex-state-plugin/core"
	pluginv1 "github.com/baiyucraft/codex-state-plugin/sdk/v1"
)

func TestForwardTransportFailureConservativelySentWithoutReplay(t *testing.T) {
	for _, phase := range []string{"tls_before_headers", "disconnect_after_request"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run(phase+"/"+method, func(t *testing.T) {
				s, h := testServer(t)
				var connections, requests atomic.Int32
				const payload = "original streaming request"
				upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil || (method == http.MethodPost && string(body) != payload) {
						t.Error("request body changed", err)
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error("hijack failed", err)
						return
					}
					_ = conn.Close()
				}))
				upstream.Config.ErrorLog = log.New(io.Discard, "", 0)
				upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateNew {
						connections.Add(1)
					}
				}
				if phase == "tls_before_headers" {
					// The production transport requires newer TLS, so Do fails before
					// request headers exist. RequestSent must still be conservative.
					upstream.TLS = &tls.Config{MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS10}
					upstream.StartTLS()
				} else {
					upstream.Start()
				}
				defer upstream.Close()
				start := completionStart(upstream.URL, "complete-transport-"+phase+"-"+method)
				start.Method = method
				start.Headers = map[string]*pluginv1.HeaderValues{core.StateHeader: {Values: []string{"client-state"}}}
				body := ""
				if method == http.MethodPost {
					body = payload
					start.HasBody = true
					start.ContentLength = -1
				}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				stream := &fakeStream{ctx: ctx, frames: forwardFrames(start, body)}
				if err := s.Forward(stream); err != nil {
					t.Fatal(err)
				}
				if len(stream.responses) != 1 {
					t.Fatal("expected one transport failure", stream.responses)
				}
				failure := stream.responses[0].GetError()
				if failure == nil || failure.Code != "upstream_transport_failed" || !failure.RequestSent {
					t.Fatal("client.Do failure was reported as safe to replay", failure)
				}
				wantRequests := int32(1)
				if phase == "tls_before_headers" {
					wantRequests = 0
				}
				if connections.Load() != 1 || requests.Load() != wantRequests {
					t.Fatalf("transport replayed: connections=%d requests=%d", connections.Load(), requests.Load())
				}
				assertCompletedOnce(t, h, start.RequestId)
			})
		}
	}
}
