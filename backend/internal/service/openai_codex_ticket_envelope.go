package service

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	codexTicketEnvelopeVersion          byte = 0x80
	codexTicketEnvelopeFixedBytes            = 57
	codexTicketEnvelopeBlockBytes            = 16
	codexTicketEnvelopeProBlocks             = 10
	codexTicketEnvelopeTeamBlocks            = 12
	codexTicketEnvelopePlanPro               = "pro"
	codexTicketEnvelopePlanTeam              = "team"
	codexTicketEnvelopeTTL                   = time.Hour
	codexTicketEnvelopeClockSkew             = 30 * time.Second
	codexTicketEnvelopeAllocationCutoff      = 30 * time.Second
)

var (
	codexTicketEnvelopeMinIssuedAt = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)
	codexTicketEnvelopeMaxIssuedAt = time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC)
)

// codexTicketEnvelope contains only validated metadata derived from an opaque
// Codex STATE value. Fingerprint identifies the decoded envelope without
// retaining or exposing the original ticket.
type codexTicketEnvelope struct {
	IssuedAt    time.Time
	ExpiresAt   time.Time
	Blocks      int
	Fingerprint string
}

// parseCodexTicketEnvelope validates the observable STATE envelope shape. It
// does not cryptographically authenticate the opaque encrypted payload.
func parseCodexTicketEnvelope(state string, plan string, now time.Time) (*codexTicketEnvelope, error) {
	envelope, err := parseCodexTicketEnvelopeShape(state, now)
	if err != nil {
		return nil, err
	}
	expectedBlocks, err := codexTicketEnvelopeBlocksForPlan(plan)
	if err != nil {
		return nil, err
	}
	if envelope.Blocks != expectedBlocks {
		return nil, fmt.Errorf("codex ticket envelope has %d blocks; %s plan requires %d", envelope.Blocks, strings.ToLower(strings.TrimSpace(plan)), expectedBlocks)
	}
	return envelope, nil
}

func parseCodexTicketEnvelopeShape(state string, now time.Time) (*codexTicketEnvelope, error) {
	if state == "" {
		return nil, fmt.Errorf("codex ticket envelope is empty")
	}
	if now.IsZero() {
		return nil, fmt.Errorf("codex ticket validation time is zero")
	}

	core := strings.TrimRight(state, "=")
	padding := len(state) - len(core)
	if core == "" || padding > 2 || strings.Contains(core, "=") {
		return nil, fmt.Errorf("codex ticket envelope has invalid base64 padding")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(core)
	if err != nil {
		return nil, fmt.Errorf("decode codex ticket envelope: %w", err)
	}
	if len(raw) < 73 || (len(raw)-codexTicketEnvelopeFixedBytes)%codexTicketEnvelopeBlockBytes != 0 {
		return nil, fmt.Errorf("codex ticket envelope has invalid decoded length %d", len(raw))
	}
	if raw[0] != codexTicketEnvelopeVersion {
		return nil, fmt.Errorf("codex ticket envelope has unsupported version 0x%02x", raw[0])
	}

	blocks := (len(raw) - codexTicketEnvelopeFixedBytes) / codexTicketEnvelopeBlockBytes

	issuedSeconds := binary.BigEndian.Uint64(raw[1:9])
	minSeconds := uint64(codexTicketEnvelopeMinIssuedAt.Unix())
	maxSeconds := uint64(codexTicketEnvelopeMaxIssuedAt.Unix())
	if issuedSeconds < minSeconds || issuedSeconds >= maxSeconds {
		return nil, fmt.Errorf("codex ticket envelope issued_at is outside the supported range")
	}
	issuedAt := time.Unix(int64(issuedSeconds), 0).UTC()
	if issuedAt.After(now.UTC().Add(codexTicketEnvelopeClockSkew)) {
		return nil, fmt.Errorf("codex ticket envelope issued_at is too far in the future")
	}

	fingerprint := sha256.Sum256(raw)
	return &codexTicketEnvelope{
		IssuedAt:    issuedAt,
		ExpiresAt:   issuedAt.Add(codexTicketEnvelopeTTL),
		Blocks:      blocks,
		Fingerprint: hex.EncodeToString(fingerprint[:]),
	}, nil
}

func isCodexTicketAbnormalEnvelope(state string, now time.Time) bool {
	envelope, err := parseCodexTicketEnvelopeShape(state, now)
	return err == nil && envelope.Blocks != codexTicketEnvelopeProBlocks && envelope.Blocks != codexTicketEnvelopeTeamBlocks
}

func codexTicketEnvelopeBlocksForPlan(plan string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case codexTicketEnvelopePlanPro:
		return codexTicketEnvelopeProBlocks, nil
	case codexTicketEnvelopePlanTeam:
		return codexTicketEnvelopeTeamBlocks, nil
	default:
		return 0, fmt.Errorf("unsupported codex ticket plan %q", plan)
	}
}

// usableAt reports whether the envelope may be assigned to a new request. A
// ticket is withheld for its final 30 seconds and cannot be used before its
// issued_at beyond the tolerated clock skew.
func (e *codexTicketEnvelope) usableAt(now time.Time) bool {
	if e == nil || now.IsZero() || e.IssuedAt.IsZero() || e.ExpiresAt.IsZero() {
		return false
	}
	now = now.UTC()
	if e.IssuedAt.After(now.Add(codexTicketEnvelopeClockSkew)) {
		return false
	}
	return now.Before(e.ExpiresAt.Add(-codexTicketEnvelopeAllocationCutoff))
}
