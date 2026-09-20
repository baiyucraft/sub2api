package core

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const DefaultEndpoint = "https://chatgpt.com/backend-api/codex/responses"

// NewHTTPClient never inherits environment proxies. dialProxy is used only by
// the synthetic harvest path; production Forward supplies only its own proxy.
func NewHTTPClient(proxyURL, dialProxy string) (*http.Client, error) {
	if ValidateProxy(proxyURL, false) != nil || ValidateProxy(dialProxy, true) != nil {
		return nil, errors.New("invalid transport proxy")
	}
	transport := &http.Transport{ForceAttemptHTTP2: false, DisableKeepAlives: true, DisableCompression: true, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 60 * time.Second, ExpectContinueTimeout: time.Second}
	if proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, errors.New("invalid transport proxy")
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	if dialProxy != "" {
		if proxyURL == "" {
			return nil, errors.New("dial proxy requires harvest proxy")
		}
		outer, err := url.Parse(dialProxy)
		if err != nil {
			return nil, errors.New("invalid dial proxy")
		}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			return connect(ctx, outer, address)
		}
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func connect(ctx context.Context, proxy *url.URL, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	port := proxy.Port()
	if port == "" {
		port = "80"
		if proxy.Scheme == "https" {
			port = "443"
		}
	}
	dialer := net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(proxy.Hostname(), port))
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { _ = rawConn.Close() })
	defer stop()
	if deadline, exists := ctx.Deadline(); exists {
		_ = conn.SetDeadline(deadline)
	}
	if proxy.Scheme == "https" {
		secure := tls.Client(conn, &tls.Config{ServerName: proxy.Hostname(), MinVersion: tls.VersionTLS12})
		if err = secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = secure
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if proxy.User != nil {
		password, _ := proxy.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(proxy.User.Username()+":"+password)))
	}
	if err = req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != 200 {
		return nil, errors.New("proxy CONNECT rejected")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

type HTTPProber struct{}

func (HTTPProber) Probe(ctx context.Context, probe ProbeRequest) (ProbeResult, error) {
	endpoint := probe.Identity.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	body, err := json.Marshal(map[string]any{"model": probe.Model, "store": false, "stream": true, "instructions": "Reply with exactly: pong", "input": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": "ping"}}}}})
	if err != nil {
		return ProbeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return ProbeResult{}, errors.New("invalid probe endpoint")
	}
	req.Header = probe.Identity.Headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", randomID())
	req.Header.Del(StateHeader)
	if probe.State != "" {
		req.Header.Set(StateHeader, probe.State)
	}
	client, err := NewHTTPClient(probe.ProxyURL, probe.DialProxyURL)
	if err != nil {
		return ProbeResult{}, err
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return ProbeResult{}, errors.New("probe transport failed")
	}
	defer response.Body.Close()
	result := ProbeResult{StatusCode: response.StatusCode, State: response.Header.Get(StateHeader)}
	if response.StatusCode != 200 {
		return result, errors.New("probe rejected")
	}
	parser := completionParser{onComplete: func(c completion) { result.Completed = true; result.Model = c.Model }}
	buffer := make([]byte, 32*1024)
	limited := io.LimitReader(response.Body, 2<<20)
	for !result.Completed {
		n, readErr := limited.Read(buffer)
		if n > 0 {
			parser.feed(buffer[:n])
		}
		if readErr != nil {
			if readErr == io.EOF {
				parser.finish()
			}
			if readErr != io.EOF {
				return result, errors.New("probe body failed")
			}
			break
		}
	}
	if !result.Completed {
		return result, errors.New("probe did not complete")
	}
	return result, nil
}
