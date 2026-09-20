package core

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
)

const observerLimit = 1 << 20

type completion struct{ Model string }
type completionParser struct {
	mode                                    byte
	buffer, data                            []byte
	overflow, skipLine, finished, completed bool
	event                                   string
	onComplete                              func(completion)
}

func (p *completionParser) feed(raw []byte) {
	if p.finished || p.completed {
		return
	}
	if p.mode == 0 {
		raw = bytes.TrimLeft(raw, " \t\r\n")
		if len(raw) == 0 {
			return
		}
		p.mode = 's'
		if raw[0] == '{' {
			p.mode = 'j'
		}
	}
	if p.mode == 'j' {
		if !p.overflow && len(p.buffer)+len(raw) <= observerLimit {
			p.buffer = append(p.buffer, raw...)
		} else {
			p.overflow = true
			p.buffer = nil
		}
		return
	}
	for len(raw) > 0 {
		end := bytes.IndexByte(raw, '\n')
		part := raw
		if end >= 0 {
			part = raw[:end]
		}
		if !p.skipLine {
			if len(p.buffer)+len(part) > observerLimit {
				p.skipLine = true
				p.overflow = true
				p.buffer = nil
			} else {
				p.buffer = append(p.buffer, part...)
			}
		}
		if end < 0 {
			return
		}
		if !p.skipLine {
			p.line(p.buffer)
		}
		p.buffer = p.buffer[:0]
		p.skipLine = false
		raw = raw[end+1:]
	}
}

func (p *completionParser) line(line []byte) {
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) == 0 {
		p.flush()
		return
	}
	if bytes.HasPrefix(line, []byte("event:")) {
		p.event = strings.TrimSpace(string(line[6:]))
		return
	}
	if !p.overflow && bytes.HasPrefix(line, []byte("data:")) {
		value := bytes.TrimPrefix(line[5:], []byte{' '})
		if len(p.data)+len(value)+1 > observerLimit {
			p.overflow = true
			p.data = nil
		} else {
			p.data = append(p.data, value...)
			p.data = append(p.data, '\n')
		}
	}
}

func (p *completionParser) flush() {
	if !p.overflow {
		p.observe(p.data, p.event)
	}
	p.data = p.data[:0]
	p.event = ""
	p.overflow = false
}

func (p *completionParser) finish() {
	if p.finished {
		return
	}
	p.finished = true
	if p.mode == 'j' && !p.overflow {
		p.observe(p.buffer, "")
	}
	// SSE dispatch requires a blank line consumed by feed. EOF/Close cannot
	// terminate an event, even when its buffered JSON happens to be complete.
	p.buffer = nil
	p.data = nil
	p.event = ""
}

func (p *completionParser) observe(raw []byte, event string) {
	if p.completed {
		return
	}
	var root struct {
		Type     string          `json:"type"`
		Object   string          `json:"object"`
		Status   string          `json:"status"`
		Model    string          `json:"model"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(raw, &root) != nil {
		return
	}
	terminal := root.Type == "response.completed" || (root.Type == "" && event == "response.completed")
	var model string
	if terminal {
		var response struct {
			Status string `json:"status"`
			Model  string `json:"model"`
		}
		if json.Unmarshal(root.Response, &response) != nil || (response.Status != "" && response.Status != "completed") {
			return
		}
		model = response.Model
	} else {
		if root.Type != "" || root.Object != "response" || root.Status != "completed" {
			return
		}
		model = root.Model
	}
	if strings.TrimSpace(model) == "" {
		return
	}
	p.completed = true
	p.onComplete(completion{Model: model})
}

type observedBody struct {
	io.ReadCloser
	mu     sync.Mutex
	parser completionParser
}

func (b *observedBody) Read(buf []byte) (int, error) {
	n, err := b.ReadCloser.Read(buf)
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > 0 {
		b.parser.feed(buf[:n])
	}
	if err == io.EOF {
		b.parser.finish()
	}
	return n, err
}

func (b *observedBody) Close() error {
	err := b.ReadCloser.Close()
	b.mu.Lock()
	b.parser.finish()
	b.mu.Unlock()
	return err
}

// ObserveResponse adds a transparent observer without reading ahead, replacing
// response bytes, replaying requests, or interpreting headers before completion.
func (e *Engine) ObserveResponse(receipt *Receipt, response *http.Response) {
	if receipt == nil || response == nil || response.Body == nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return
	}
	copyReceipt := *receipt
	headerAbnormal := abnormal(response.Header.Get(StateHeader), e.opts.Now())
	response.Body = &observedBody{ReadCloser: response.Body, parser: completionParser{onComplete: func(result completion) {
		reason := ""
		if result.Model != copyReceipt.Guard.Model {
			reason = "model_mismatch"
		} else if headerAbnormal {
			reason = "state_envelope"
		}
		_ = e.RecordCompletion(copyReceipt, reason)
	}}}
}
