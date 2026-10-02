package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractContentModerationInputTypeSafeSystemOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"string", `{"state":"plain text"}`, "plain text"},
		{"object", `{"state":{"title":"hello","nested":{"body":"world"}}}`, "hello world"},
		{"array", `{"state":["first",{"text":"second"},3]}`, "first second"},
		{"questions", `{"state":"state text","questions":{"c":{"type":"choice","instructions":"pick one","criteria":{"label a":{"description":"desc a"},"label b":null}},"s":{"type":"score","criteria":["low",{"text":"high"}]},"n":{"type":"noul","instructions":["judge"],"criteria":{"true":"yes"}}}}`, "state text pick one label a desc a label b low high judge yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := ExtractContentModerationInput(ContentModerationProtocolTypeSafeSystemOne, []byte(tc.body))
			require.Equal(t, tc.want, input.Text)
			require.Empty(t, input.Images)
		})
	}
}

func TestExtractContentModerationInputTypeSafeSystemOneKeepsReminderText(t *testing.T) {
	body := `{"state":"<system-reminder>hidden payload</system-reminder>","questions":{"q":{"type":"noul","instructions":"<system-reminder>hidden instructions</system-reminder>"}}}`
	input := ExtractContentModerationInput(ContentModerationProtocolTypeSafeSystemOne, []byte(body))
	require.Contains(t, input.Text, "hidden payload")
	require.Contains(t, input.Text, "hidden instructions")
}
