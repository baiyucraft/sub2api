package core

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPProberRequiresCompletedModelAndPreservesHeaders(t *testing.T) {
	var count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != testModel || r.Header.Get("Authorization") != "Bearer test" || r.Header.Get(StateHeader) != "candidate" {
			t.Error("probe identity changed")
		}
		w.Header().Set(StateHeader, "new-state")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"response\":{\"model\":\"gpt-6-astra\",\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()
	result, err := (HTTPProber{}).Probe(context.Background(), ProbeRequest{Identity: Identity{Endpoint: server.URL, Headers: http.Header{"Authorization": {"Bearer test"}}}, Model: testModel, State: "candidate"})
	if err != nil || !result.Completed || result.Model != testModel || result.State != "new-state" || count.Load() != 1 {
		t.Fatal(result, err)
	}
}

func TestHTTPProberRejectsUnterminatedSSE(t *testing.T) {
	const event = `data: {"type":"response.completed","response":{"status":"completed","model":"gpt-6-astra"}}`
	for _, tc := range []struct {
		name, body string
		completed  bool
	}{
		{"closed", event + "\n\n", true},
		{"missing_blank_line", event + "\n", false},
		{"missing_lf", event, false},
		{"truncated_json", event[:len(event)-1] + "\n\n", false},
		{"complete_json", `{"object":"response","status":"completed","model":"gpt-6-astra"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(StateHeader, "candidate")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			result, err := (HTTPProber{}).Probe(ctx, ProbeRequest{Identity: Identity{Endpoint: server.URL}, Model: testModel})
			if result.Completed != tc.completed || (err == nil) != tc.completed || result.State != "candidate" {
				t.Fatal("incorrect probe completion", result, err)
			}
			if tc.completed && result.Model != testModel {
				t.Fatal("completed model changed")
			}
		})
	}
}

func TestHTTPChainedProxyUsesOuterOnlyForHarvest(t *testing.T) {
	var dynamicCalls, outerCalls atomic.Int32
	dynamic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dynamicCalls.Add(1)
		_, _ = io.WriteString(w, "proxied bytes")
	}))
	defer dynamic.Close()
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outerCalls.Add(1)
		if r.Method != "CONNECT" {
			t.Error("not CONNECT")
			http.Error(w, "bad", 400)
			return
		}
		conn, err := net.DialTimeout("tcp", r.Host, time.Second)
		if err != nil {
			http.Error(w, "dial", 502)
			return
		}
		client, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = conn.Close()
			return
		}
		defer client.Close()
		defer conn.Close()
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(conn, buffer); _ = conn.Close(); close(done) }()
		_, _ = io.Copy(client, conn)
		_ = client.Close()
		<-done
	}))
	defer outer.Close()
	client, err := NewHTTPClient(dynamic.URL, outer.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	response, err := client.Get("http://does-not-resolve.invalid/test")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(raw) != "proxied bytes" || outerCalls.Load() != 1 || dynamicCalls.Load() != 1 {
		t.Fatal("proxy chain bypassed")
	}
	client, err = NewHTTPClient(dynamic.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	response, err = client.Get("http://does-not-resolve.invalid/test")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if outerCalls.Load() != 1 {
		t.Fatal("business transport used outer proxy")
	}
}

func TestConnectPreservesBufferedBytes(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			return
		}
		_ = r.Body.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\n\r\nPREBUFFERED")
	}()
	client, err := NewHTTPClient("http://inner.invalid:80", "http://"+listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	transport := client.Transport.(*http.Transport)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := transport.DialContext(ctx, "tcp", "inner.invalid:80")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(conn)
	_ = conn.Close()
	<-done
	if string(raw) != "PREBUFFERED" {
		t.Fatalf("lost tunnel bytes %q", raw)
	}
}

func TestObserverOversizedFrameDoesNotPoisonFollowingCompletedEvent(t *testing.T) {
	var completed int
	p := completionParser{onComplete: func(c completion) { completed++ }}
	p.feed([]byte("data: " + strings.Repeat("x", observerLimit+1) + "\n\n"))
	p.feed([]byte("data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-6-astra\"}}\n\n"))
	p.finish()
	if completed != 1 {
		t.Fatal(completed)
	}
}

// Migrates TestCodexTicketConnectCancelsStalledProxy and adds explicit cancel
// coverage. Both cases must close the socket, not just abandon a goroutine.
func TestConnectCancelsStalledProxy(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			entered, done := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				if _, err = http.ReadRequest(bufio.NewReader(conn)); err != nil {
					return
				}
				close(entered)
				_, _ = io.Copy(io.Discard, conn)
			}()
			proxy, err := url.Parse("http://" + listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if mode == "deadline" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 50*time.Millisecond)
				defer stop()
			} else {
				go func() {
					select {
					case <-entered:
						cancel()
					case <-ctx.Done():
					}
				}()
			}
			started := time.Now()
			conn, err := connect(ctx, proxy, "example.invalid:443")
			if err == nil || conn != nil || time.Since(started) >= time.Second {
				t.Fatalf("stalled CONNECT did not cancel: conn=%v err=%v elapsed=%v", conn, err, time.Since(started))
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancellation did not close stalled proxy socket")
			}
		})
	}
}
