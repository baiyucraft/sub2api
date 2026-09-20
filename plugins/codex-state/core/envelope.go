package core

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const (
	TicketTTL        = time.Hour
	FutureSkew       = 30 * time.Second
	AllocationCutoff = 30 * time.Second
	RefreshBefore    = 10 * time.Minute
	MaxAttempts      = 8
	RetryCooldown    = 5 * time.Minute
)

type Envelope struct {
	IssuedAt, ExpiresAt time.Time
	Blocks              int
	Fingerprint         string
}

// ParseEnvelope validates observable framing and internal time, not encryption.
// The envelope implementation is derived from this repository's local service.
func ParseEnvelope(state, plan string, now time.Time) (Envelope, error) {
	env, err := parseShape(state, now)
	if err != nil {
		return env, err
	}
	expected := 0
	switch plan {
	case "pro":
		expected = 10
	case "team":
		expected = 12
	}
	if expected == 0 || env.Blocks != expected {
		return Envelope{}, errors.New("state plan mismatch")
	}
	return env, nil
}

func parseShape(state string, now time.Time) (Envelope, error) {
	if state == "" || len(state) > 8192 || now.IsZero() {
		return Envelope{}, errors.New("invalid state envelope")
	}
	core := strings.TrimRight(state, "=")
	if len(state)-len(core) > 2 || strings.Contains(core, "=") {
		return Envelope{}, errors.New("invalid state encoding")
	}
	// Strict base64 decoding still ignores CR/LF. Reject anything outside the
	// URL-safe alphabet before decoding instead of normalizing transport bytes.
	for i := 0; i < len(core); i++ {
		c := core[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return Envelope{}, errors.New("invalid state encoding")
		}
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(core)
	if err != nil || len(raw) < 73 || (len(raw)-57)%16 != 0 || raw[0] != 0x80 {
		return Envelope{}, errors.New("invalid state envelope")
	}
	if len(state) != len(core) && (len(state)%4 != 0 || len(state)-len(core) != (4-len(core)%4)%4) {
		return Envelope{}, errors.New("invalid state padding")
	}
	seconds := binary.BigEndian.Uint64(raw[1:9])
	if seconds < 1577836800 || seconds >= 4102444800 {
		return Envelope{}, errors.New("invalid state timestamp")
	}
	issued := time.Unix(int64(seconds), 0).UTC()
	if issued.After(now.Add(FutureSkew)) {
		return Envelope{}, errors.New("state issued in future")
	}
	digest := sha256.Sum256(raw)
	return Envelope{IssuedAt: issued, ExpiresAt: issued.Add(TicketTTL), Blocks: (len(raw) - 57) / 16, Fingerprint: hex.EncodeToString(digest[:])}, nil
}

func (e Envelope) Usable(now time.Time) bool {
	return !now.IsZero() && !e.IssuedAt.IsZero() && !e.IssuedAt.After(now.Add(FutureSkew)) && now.Before(e.ExpiresAt.Add(-AllocationCutoff))
}

func usable(t *Ticket, plan string, now time.Time) bool {
	if t == nil || t.Version == 0 || t.CapturedAt.IsZero() {
		return false
	}
	env, err := ParseEnvelope(t.State, plan, now)
	return err == nil && env.Usable(now) && env.Fingerprint == t.Fingerprint && env.IssuedAt.Equal(t.IssuedAt) && env.ExpiresAt.Equal(t.ExpiresAt)
}

func abnormal(state string, now time.Time) bool {
	env, err := parseShape(state, now)
	return err == nil && env.Blocks != 10 && env.Blocks != 12
}
