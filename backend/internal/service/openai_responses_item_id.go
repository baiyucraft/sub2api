package service

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openAIResponsesInputItemIDPrefix centralizes the temporary Responses replay
// compatibility contract. Keep this small and isolated so an upstream fix can
// replace/remove the workaround without changing unrelated Codex transforms.
func openAIResponsesInputItemIDPrefix(itemType string) (string, bool) {
	switch itemType {
	case "message":
		return "msg", true
	case "reasoning":
		return "rs", true
	case "custom_tool_call":
		// Temporary compatibility layer for the upstream Responses contract:
		// custom_tool_call item ids use the ctc_ namespace. Keep this branch
		// isolated so a future upstream fix can replace it in one commit.
		return "ctc_", true
	default:
		if isCodexToolCallInputType(itemType) {
			return "fc_", true
		}
		return "", false
	}
}

// Invalid replayed IDs are removed rather than rewritten because a fabricated
// msg/fc/ctc ID may point at a different upstream object.
func shouldStripOpenAIResponsesInputItemID(itemType, id string) bool {
	if id == "" {
		return false
	}
	if prefix, ok := openAIResponsesInputItemIDPrefix(itemType); ok {
		return !strings.HasPrefix(id, prefix)
	}
	return false
}

func sanitizeOpenAIResponsesInputItemIDs(body []byte) ([]byte, bool, error) {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, false, nil
	}

	items := make([][]byte, 0)
	changed := false
	var sanitizeErr error
	index := 0
	input.ForEach(func(_, item gjson.Result) bool {
		currentIndex := index
		index++
		itemBody := []byte(item.Raw)
		if item.IsObject() {
			itemType := item.Get("type")
			id := item.Get("id")
			if itemType.Type == gjson.String && id.Type == gjson.String &&
				shouldStripOpenAIResponsesInputItemID(itemType.String(), id.String()) {
				itemBody, sanitizeErr = sjson.DeleteBytes(itemBody, "id")
				if sanitizeErr != nil {
					sanitizeErr = fmt.Errorf("delete input.%d.id: %w", currentIndex, sanitizeErr)
					return false
				}
				changed = true
			}
		}
		items = append(items, itemBody)
		return true
	})
	if sanitizeErr != nil {
		return nil, false, sanitizeErr
	}
	if !changed {
		return body, false, nil
	}

	rebuiltInput := make([]byte, 0, len(input.Raw))
	rebuiltInput = append(rebuiltInput, '[')
	for i, item := range items {
		if i > 0 {
			rebuiltInput = append(rebuiltInput, ',')
		}
		rebuiltInput = append(rebuiltInput, item...)
	}
	rebuiltInput = append(rebuiltInput, ']')

	sanitized, err := sjson.SetRawBytes(body, "input", rebuiltInput)
	if err != nil {
		return nil, false, fmt.Errorf("replace sanitized input: %w", err)
	}
	return sanitized, true, nil
}
