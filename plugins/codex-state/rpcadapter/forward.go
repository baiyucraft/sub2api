package rpcadapter

import (
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/baiyucraft/codex-state-plugin/core"
	pluginv1 "github.com/baiyucraft/codex-state-plugin/sdk/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type requestBody struct {
	stream  grpc.BidiStreamingServer[pluginv1.ForwardRequest, pluginv1.ForwardResponse]
	pending []byte
	ended   bool
	closed  atomic.Bool
}

func (b *requestBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(b.pending) == 0 {
		if b.ended || b.closed.Load() {
			return 0, io.EOF
		}
		frame, err := b.stream.Recv()
		if err != nil {
			if err == io.EOF {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		switch value := frame.Frame.(type) {
		case *pluginv1.ForwardRequest_BodyChunk:
			b.pending = value.BodyChunk
		case *pluginv1.ForwardRequest_BodyEnd:
			if !value.BodyEnd {
				return 0, io.ErrUnexpectedEOF
			}
			b.ended = true
		default:
			return 0, io.ErrUnexpectedEOF
		}
	}
	n := copy(p, b.pending)
	b.pending = b.pending[n:]
	return n, nil
}
func (b *requestBody) Close() error { b.closed.Store(true); return nil }

func (s *Server) Forward(stream grpc.BidiStreamingServer[pluginv1.ForwardRequest, pluginv1.ForwardResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "first frame must be start")
	}
	// Registered before response cleanup: every valid request is acknowledged
	// only after upstream Close and synchronous watchdog persistence finish.
	defer s.completeRequest(start.RequestId)
	var sent atomic.Bool
	fail := func(code string) error {
		return stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Error{Error: &pluginv1.ForwardResponseError{Code: code, Message: code, RequestSent: sent.Load()}}})
	}
	if !s.engine.RuntimeActive() {
		return fail("plugin_admission_runtime_inactive")
	}
	if strconv.FormatUint(start.ConfigRevision, 10) != s.engine.ConfigRevision() {
		return fail("plugin_admission_config_revision_mismatch")
	}
	headers := decodeHeaders(start.Headers)
	receipt, err := s.engine.Prepare(stream.Context(), start.AccountId, start.OutboundModel, start.IdentityRevision, headers)
	if err != nil {
		return fail("plugin_admission_state_unavailable")
	}
	// Identity/state RPCs can overlap Apply. Recheck before starting upstream.
	if !s.engine.RuntimeActive() {
		return fail("plugin_admission_runtime_inactive")
	}
	if strconv.FormatUint(start.ConfigRevision, 10) != s.engine.ConfigRevision() {
		return fail("plugin_admission_config_revision_mismatch")
	}
	var body io.ReadCloser
	if start.HasBody {
		body = &requestBody{stream: stream}
	}
	req, err := http.NewRequestWithContext(stream.Context(), start.Method, start.Url, body)
	if err != nil {
		return fail("invalid_forward_request")
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return fail("invalid_forward_request")
	}
	req.Header = headers
	req.Host = start.Host
	req.ContentLength = start.ContentLength
	// A streaming body has no GetBody function: the transport cannot replay it.
	client, err := core.NewHTTPClient(start.ProxyUrl, "")
	if err != nil {
		return fail("invalid_forward_proxy")
	}
	defer client.CloseIdleConnections()
	started := time.Now()
	// Once transport owns the request, a failure cannot prove that no bytes
	// reached upstream. Conservatively prohibit host replay from this point.
	sent.Store(true)
	response, err := client.Do(req)
	if err != nil {
		return fail("upstream_transport_failed")
	}
	s.engine.ObserveResponse(receipt, response)
	var closeOnce sync.Once
	finishResponse := func() { closeOnce.Do(func() { _ = response.Body.Close() }) }
	defer finishResponse()
	err = stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{StatusCode: int32(response.StatusCode), Status: response.Status, Protocol: response.Proto, ProtocolMajor: int32(response.ProtoMajor), ProtocolMinor: int32(response.ProtoMinor), Headers: encodeHeaders(response.Header), ContentLength: response.ContentLength}}})
	if err != nil {
		return err
	}
	buffer := make([]byte, 32*1024)
	var received int64
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			received += int64(n)
			if err = stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_BodyChunk{BodyChunk: append([]byte(nil), buffer[:n]...)}}); err != nil {
				return err
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return fail("upstream_body_failed")
			}
			break
		}
	}
	// End is also a host completion proof. Close first so any complete JSON
	// document has finished its bounded observer persistence before End.
	finishResponse()
	return stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_End{End: &pluginv1.ForwardResponseEnd{BytesReceived: received, DurationMs: time.Since(started).Milliseconds()}}})
}
