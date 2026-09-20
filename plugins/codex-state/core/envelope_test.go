package core

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestEnvelopeRejectsNonURLAlphabetAndInvalidPadding(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	raw, err := base64.URLEncoding.DecodeString(stateAt(now, 10, 1))
	if err != nil {
		t.Fatal(err)
	}
	// Exercise both URL-safe characters in an otherwise valid local envelope.
	raw[9], raw[10], raw[11] = 0xfb, 0xff, 0xff
	padded := base64.URLEncoding.EncodeToString(raw)
	unpadded := base64.RawURLEncoding.EncodeToString(raw)
	expected, err := ParseEnvelope(padded, "pro", now)
	if err != nil {
		t.Fatal(err)
	}
	if env, err := ParseEnvelope(unpadded, "pro", now); err != nil || env != expected || env.Blocks != 10 || !env.IssuedAt.Equal(now) || !env.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatal("padded/unpadded framing or time rules changed", env, err)
	}
	for _, tc := range []struct {
		name, value string
	}{
		{"cr", "\r"}, {"lf", "\n"}, {"crlf", "\r\n"},
		{"space", " "}, {"tab", "\t"}, {"nul", "\x00"},
		{"non_ascii_space", "\u00a0"}, {"non_ascii_letter", "\u00e9"},
		{"standard_plus", "+"}, {"standard_slash", "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, original := range []string{padded, unpadded} {
				for _, at := range []int{0, len(original) / 2, len(original)} {
					state := original[:at] + tc.value + original[at:]
					if _, err := ParseEnvelope(state, "pro", now); err == nil {
						t.Fatalf("accepted invalid byte sequence at offset %d", at)
					}
				}
			}
		})
	}
	for _, tc := range []struct {
		name, state string
	}{
		{"middle_padding", unpadded[:8] + "=" + unpadded[8:]},
		{"partial_padding", unpadded + "="},
		{"excess_padding", unpadded + "==="},
		{"suffix_after_padding", padded + "A"},
		{"newline_between_padding", unpadded + "=\n="},
		{"standard_base64", strings.NewReplacer("-", "+", "_", "/").Replace(padded)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseEnvelope(tc.state, "pro", now); err == nil {
				t.Fatal("accepted invalid alphabet or padding")
			}
		})
	}
	team, err := ParseEnvelope(stateAt(now, 12, 1), "team", now)
	if err != nil || team.Blocks != 12 || !team.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatal("team framing or time rules changed", team, err)
	}
}
