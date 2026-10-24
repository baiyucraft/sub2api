package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// parseMonitorSSEFrame stages a complete, already bounded SSE frame. A provider
// that omits event separators may put several JSON objects in it; none of their
// text, timing or terminal state becomes visible until the entire frame passes.
func parseMonitorSSEFrame(dataLines []string, provider, apiMode string, started time.Time, previous monitorStreamResult) (monitorStreamResult, error) {
	staged := monitorStreamResult{
		TTFTMs:       previous.TTFTMs,
		Completed:    previous.Completed,
		FinishReason: previous.FinishReason,
	}
	var output strings.Builder
	consume := func(payload []byte) error {
		if payload == nil { // Only a complete, independent data: [DONE] line.
			staged.Completed = true
			return nil
		}
		candidate := staged
		var chunk strings.Builder
		done, err := parseMonitorSSEPayload(payload, provider, apiMode, &chunk, &candidate, started)
		if err != nil {
			return err
		}
		if staged.Completed {
			if chunk.Len() > 0 {
				return errors.New("monitor stream reported text after completion")
			}
			// Usage, metadata and repeated terminal markers may follow a finish
			// chunk. Preserve the first terminal's reason and the existing TTFT.
			return nil
		}
		candidate.Completed = done
		staged = candidate
		output.WriteString(chunk.String())
		return nil
	}

	data := strings.Join(dataLines, "\n")
	switch {
	case strings.TrimSpace(data) == "[DONE]":
		staged.Completed = true
	case json.Valid([]byte(data)):
		// Standard SSE multiline JSON takes precedence. A valid JSON event
		// with an invalid protocol shape must fail without a second attempt.
		if err := consume([]byte(data)); err != nil {
			return monitorStreamResult{}, err
		}
	default:
		if err := forEachMonitorCompatPayload(dataLines, provider, apiMode, consume); err != nil {
			return monitorStreamResult{}, err
		}
	}
	staged.Text = output.String()
	return staged, nil
}

// forEachMonitorCompatPayload accepts only fully consumed JSON object sequences
// and independent [DONE] lines. It does not split JSON strings or repair damaged
// input. Decoding one object at a time avoids retaining an unbounded object list.
func forEachMonitorCompatPayload(dataLines []string, provider, apiMode string, consume func([]byte) error) error {
	consumed := false
	decode := func(lines []string) error {
		decoder := json.NewDecoder(strings.NewReader(strings.Join(lines, "\n")))
		for {
			var payload json.RawMessage
			if err := decoder.Decode(&payload); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return monitorSSEDecodeError(provider, apiMode, err)
			}
			if err := consume(payload); err != nil {
				return err
			}
			consumed = true
		}
	}

	segmentStart := 0
	for index, line := range dataLines {
		if strings.TrimSpace(line) != "[DONE]" {
			continue
		}
		if err := decode(dataLines[segmentStart:index]); err != nil {
			return err
		}
		if err := consume(nil); err != nil {
			return err
		}
		consumed = true
		segmentStart = index + 1
	}
	if err := decode(dataLines[segmentStart:]); err != nil {
		return err
	}
	if !consumed {
		return monitorSSEDecodeError(provider, apiMode, io.ErrUnexpectedEOF)
	}
	return nil
}

func parseMonitorSSEPayload(data []byte, provider, apiMode string, output *strings.Builder, result *monitorStreamResult, started time.Time) (bool, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return false, monitorSSEDecodeError(provider, apiMode, errors.New("payload must be a JSON object"))
	}
	switch {
	case (provider == MonitorProviderOpenAI || provider == MonitorProviderZhipu) && apiMode == MonitorAPIModeResponses:
		return parseMonitorResponsesEvent(data, output, result, started)
	case provider == MonitorProviderAnthropic:
		return parseMonitorAnthropicEvent(data, output, result, started)
	case provider == MonitorProviderGemini:
		return parseMonitorGeminiEvent(data, output, result, started)
	default:
		return parseMonitorChatEvent(data, output, result, started)
	}
}

func monitorSSEHasError(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte(`""`))
}

func monitorSSEDecodeError(provider, apiMode string, err error) error {
	protocol := "chat"
	switch {
	case (provider == MonitorProviderOpenAI || provider == MonitorProviderZhipu) && apiMode == MonitorAPIModeResponses:
		protocol = "responses"
	case provider == MonitorProviderAnthropic:
		protocol = "Anthropic"
	case provider == MonitorProviderGemini:
		protocol = "Gemini"
	}
	return fmt.Errorf("decode monitor %s event: %w", protocol, err)
}
