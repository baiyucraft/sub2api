package migrations

import (
	"strings"
	"testing"
)

func TestUpstreamConfidenceMultiprobeMigrationAddsEvidence(t *testing.T) {
	sql, err := FS.ReadFile("250_upstream_confidence_multiprobe_v2.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToLower(string(sql))
	if !strings.Contains(normalized, "add column if not exists confidence_evidence jsonb") {
		t.Fatalf("migration does not add confidence_evidence JSONB")
	}
	if !strings.Contains(normalized, "openai-juice-multiprobe-v2") {
		t.Fatalf("migration does not index v2 observations")
	}
}
