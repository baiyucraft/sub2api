package service

import (
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseCodexTicketEnvelopeValidPlansAndPadding(t *testing.T) {
	now := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)
	issuedAt := now.Add(-5 * time.Minute)

	proPadded := buildCodexTicketEnvelopeForTest(issuedAt, codexTicketEnvelopeProBlocks, true)
	proRaw := strings.TrimRight(proPadded, "=")
	pro, err := parseCodexTicketEnvelope(proPadded, codexTicketEnvelopePlanPro, now)
	require.NoError(t, err)
	require.Equal(t, issuedAt, pro.IssuedAt)
	require.Equal(t, issuedAt.Add(time.Hour), pro.ExpiresAt)
	require.Equal(t, codexTicketEnvelopeProBlocks, pro.Blocks)
	require.Len(t, pro.Fingerprint, 64)
	require.True(t, pro.usableAt(now))

	proWithoutPadding, err := parseCodexTicketEnvelope(proRaw, " PRO ", now)
	require.NoError(t, err)
	require.Equal(t, pro.Fingerprint, proWithoutPadding.Fingerprint)

	teamState := buildCodexTicketEnvelopeForTest(issuedAt, codexTicketEnvelopeTeamBlocks, false)
	team, err := parseCodexTicketEnvelope(teamState, codexTicketEnvelopePlanTeam, now)
	require.NoError(t, err)
	require.Equal(t, codexTicketEnvelopeTeamBlocks, team.Blocks)
	require.True(t, team.usableAt(now))
}

func TestParseCodexTicketEnvelopeRejectsInvalidEncodingAndVersion(t *testing.T) {
	now := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)
	valid := buildCodexTicketEnvelopeForTest(now, codexTicketEnvelopeProBlocks, false)

	_, err := parseCodexTicketEnvelope(valid[:10]+"+"+valid[11:], codexTicketEnvelopePlanPro, now)
	require.ErrorContains(t, err, "decode codex ticket envelope")

	_, err = parseCodexTicketEnvelope(valid+"===", codexTicketEnvelopePlanPro, now)
	require.ErrorContains(t, err, "invalid base64 padding")

	raw, err := base64.RawURLEncoding.Strict().DecodeString(valid)
	require.NoError(t, err)
	raw[0] = 0x81
	invalidVersion := base64.RawURLEncoding.EncodeToString(raw)
	_, err = parseCodexTicketEnvelope(invalidVersion, codexTicketEnvelopePlanPro, now)
	require.ErrorContains(t, err, "unsupported version")
}

func TestParseCodexTicketEnvelopeValidatesBlockCountAndPlan(t *testing.T) {
	now := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)
	pro := buildCodexTicketEnvelopeForTest(now, codexTicketEnvelopeProBlocks, false)
	team := buildCodexTicketEnvelopeForTest(now, codexTicketEnvelopeTeamBlocks, false)

	_, err := parseCodexTicketEnvelope(team, codexTicketEnvelopePlanPro, now)
	require.ErrorContains(t, err, "pro plan requires 10")

	_, err = parseCodexTicketEnvelope(pro, codexTicketEnvelopePlanTeam, now)
	require.ErrorContains(t, err, "team plan requires 12")

	_, err = parseCodexTicketEnvelope(pro, "enterprise", now)
	require.ErrorContains(t, err, "unsupported codex ticket plan")

	malformed := make([]byte, 72)
	malformed[0] = codexTicketEnvelopeVersion
	binary.BigEndian.PutUint64(malformed[1:9], uint64(now.Unix()))
	_, err = parseCodexTicketEnvelope(base64.RawURLEncoding.EncodeToString(malformed), codexTicketEnvelopePlanPro, now)
	require.ErrorContains(t, err, "invalid decoded length")
}

func TestParseCodexTicketEnvelopeValidatesIssuedAt(t *testing.T) {
	now := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)

	withinSkew := buildCodexTicketEnvelopeForTest(now.Add(30*time.Second), codexTicketEnvelopeProBlocks, false)
	_, err := parseCodexTicketEnvelope(withinSkew, codexTicketEnvelopePlanPro, now)
	require.NoError(t, err)

	beyondSkew := buildCodexTicketEnvelopeForTest(now.Add(31*time.Second), codexTicketEnvelopeProBlocks, false)
	_, err = parseCodexTicketEnvelope(beyondSkew, codexTicketEnvelopePlanPro, now)
	require.ErrorContains(t, err, "too far in the future")

	beforeRange := buildCodexTicketEnvelopeForTest(codexTicketEnvelopeMinIssuedAt.Add(-time.Second), codexTicketEnvelopeProBlocks, false)
	_, err = parseCodexTicketEnvelope(beforeRange, codexTicketEnvelopePlanPro, now)
	require.ErrorContains(t, err, "outside the supported range")

	atUpperBound := buildCodexTicketEnvelopeForTest(codexTicketEnvelopeMaxIssuedAt, codexTicketEnvelopeProBlocks, false)
	_, err = parseCodexTicketEnvelope(atUpperBound, codexTicketEnvelopePlanPro, codexTicketEnvelopeMaxIssuedAt)
	require.ErrorContains(t, err, "outside the supported range")
}

func TestCodexTicketEnvelopeUsableAtExpiryBoundary(t *testing.T) {
	now := time.Date(2026, time.September, 19, 8, 0, 0, 0, time.UTC)

	usableState := buildCodexTicketEnvelopeForTest(now.Add(-time.Hour+31*time.Second), codexTicketEnvelopeProBlocks, false)
	usable, err := parseCodexTicketEnvelope(usableState, codexTicketEnvelopePlanPro, now)
	require.NoError(t, err)
	require.True(t, usable.usableAt(now))

	cutoffState := buildCodexTicketEnvelopeForTest(now.Add(-time.Hour+30*time.Second), codexTicketEnvelopeProBlocks, false)
	cutoff, err := parseCodexTicketEnvelope(cutoffState, codexTicketEnvelopePlanPro, now)
	require.NoError(t, err)
	require.False(t, cutoff.usableAt(now))

	expiredState := buildCodexTicketEnvelopeForTest(now.Add(-time.Hour), codexTicketEnvelopeTeamBlocks, false)
	expired, err := parseCodexTicketEnvelope(expiredState, codexTicketEnvelopePlanTeam, now)
	require.NoError(t, err)
	require.False(t, expired.usableAt(now))
}

func buildCodexTicketEnvelopeForTest(issuedAt time.Time, blocks int, padded bool) string {
	raw := make([]byte, codexTicketEnvelopeFixedBytes+blocks*codexTicketEnvelopeBlockBytes)
	raw[0] = codexTicketEnvelopeVersion
	binary.BigEndian.PutUint64(raw[1:9], uint64(issuedAt.Unix()))
	for i := 9; i < len(raw); i++ {
		raw[i] = byte((i*31 + blocks) % 251)
	}
	if padded {
		return base64.URLEncoding.EncodeToString(raw)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}
