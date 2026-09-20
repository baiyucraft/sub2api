package core

import "testing"

func TestCompletionParserSSERequiresBlankLine(t *testing.T) {
	const event = `data: {"type":"response.completed","response":{"status":"completed","model":"gpt-6-astra"}}`
	const named = "event: response.completed\ndata: {\"response\":{\"status\":\"completed\",\"model\":\"gpt-6-astra\"}}"
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"closed_lf", event + "\n\n", 1},
		{"closed_crlf", event + "\r\n\r\n", 1},
		{"missing_blank_line", event + "\n", 0},
		{"missing_lf", event, 0},
		{"missing_crlf_blank_line", event + "\r\n", 0},
		{"partial_crlf_delimiter", event + "\r\n\r", 0},
		{"truncated_json_with_delimiter", event[:len(event)-1] + "\n\n", 0},
		{"truncated_json_without_lf", event[:len(event)-1], 0},
		{"named_closed", named + "\n\n", 1},
		{"named_missing_blank_line", named + "\n", 0},
		{"named_missing_lf", named, 0},
		{"comment_without_delimiter", event + "\n: comment\n", 0},
		{"prior_partial_event", "data: {\"type\":\"response.created\"}\n\n" + event + "\n", 0},
		{"completed_then_truncated_tail", event + "\n\ndata: {", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for split := 0; split <= len(tc.body); split++ {
				completed := 0
				parser := completionParser{onComplete: func(result completion) {
					completed++
					if result.Model != testModel {
						t.Error("completed model changed")
					}
				}}
				parser.feed([]byte(tc.body[:split]))
				parser.feed([]byte(tc.body[split:]))
				if completed != tc.want {
					t.Fatalf("split=%d: feed completed=%d want=%d", split, completed, tc.want)
				}
				parser.finish()
				parser.finish()
				parser.feed([]byte("\n\n"))
				if completed != tc.want || len(parser.buffer) != 0 || len(parser.data) != 0 || parser.event != "" {
					t.Fatalf("split=%d: finish retained or dispatched an unfinished SSE event", split)
				}
			}
		})
	}
}

func TestCompletionParserFinishRequiresCompleteJSONDocument(t *testing.T) {
	const document = `{"object":"response","status":"completed","model":"gpt-6-astra"}`
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"complete", document, 1},
		{"complete_with_whitespace", " \n" + document + "\r\n", 1},
		{"truncated", document[:len(document)-1], 0},
		{"trailing_json", document + `{}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completed := 0
			parser := completionParser{onComplete: func(completion) { completed++ }}
			for i := range len(tc.body) {
				parser.feed([]byte(tc.body[i : i+1]))
			}
			if completed != 0 {
				t.Fatal("JSON completion fired before finish")
			}
			parser.finish()
			parser.finish()
			if completed != tc.want {
				t.Fatalf("completed=%d want=%d", completed, tc.want)
			}
		})
	}
}
