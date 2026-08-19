package repository

// This file contains the migration catalog/planner contract used by release
// tooling.  The application runner still owns the database lock and the
// execution details, while this layer provides one deterministic view of the
// candidate catalog and the database state.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
)

const migrationChecksumPolicyVersion = "sub2api-migration-checksum-policy-v1"

// MigrationCatalogEntry is one non-empty SQL migration in a candidate image.
// The content itself is intentionally not exposed in JSON; checksum and
// execution mode are sufficient for release planning and signing.
type MigrationCatalogEntry struct {
	Filename         string `json:"filename"`
	Checksum         string `json:"checksum"`
	NonTransactional bool   `json:"non_transactional"`
}

// MigrationCatalog is the deterministic, filename-sorted migration manifest.
type MigrationCatalog struct {
	Entries []MigrationCatalogEntry `json:"entries"`
	SHA256  string                  `json:"sha256"`
}

// MigrationRecord is the persisted schema_migrations representation needed by
// the planner.  applied_at is deliberately omitted because it does not affect
// migration identity or execution order.
type MigrationRecord struct {
	Filename string `json:"filename"`
	Checksum string `json:"checksum"`
}

// MigrationPlanItem describes either an already verified or a pending
// migration. Status is "existing", "verified-compatible", or "pending".
type MigrationPlanItem struct {
	Filename         string `json:"filename"`
	Checksum         string `json:"checksum"`
	NonTransactional bool   `json:"non_transactional"`
	Status           string `json:"status"`
}

// MigrationChecksumConflict is a hard-stop mismatch between the database and
// candidate file.
type MigrationChecksumConflict struct {
	Filename          string `json:"filename"`
	DatabaseChecksum  string `json:"database_checksum"`
	CandidateChecksum string `json:"candidate_checksum"`
}

// MigrationPlan is the read-only JSON contract consumed by Gate v2 tooling.
// A non-empty Conflicts or Unknown list is never safe to execute.
type MigrationPlan struct {
	Catalog                   MigrationCatalog            `json:"catalog"`
	CatalogSHA256             string                      `json:"catalog_sha256"`
	ChecksumPolicyVersion     string                      `json:"checksum_policy_version"`
	ChecksumPolicySHA256      string                      `json:"checksum_policy_sha256"`
	DatabaseHighWatermark     string                      `json:"database_high_watermark,omitempty"`
	Existing                  []MigrationPlanItem         `json:"existing"`
	Pending                   []MigrationPlanItem         `json:"pending"`
	Conflicts                 []MigrationChecksumConflict `json:"conflicts"`
	Unknown                   []MigrationRecord           `json:"unknown"`
	ExistingChecksumsVerified bool                        `json:"existing_checksums_verified"`
}

// BuildMigrationCatalog discovers, normalizes, validates, and hashes all
// non-empty *.sql files.  Discovery and checksum calculation are intentionally
// shared by planner and runner so a release cannot plan one catalog and execute
// another.
func BuildMigrationCatalog(fsys fs.FS) (MigrationCatalog, error) {
	if fsys == nil {
		return MigrationCatalog{}, errors.New("nil migration filesystem")
	}
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return MigrationCatalog{}, fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(files)
	entries := make([]MigrationCatalogEntry, 0, len(files))
	for _, name := range files {
		contentBytes, err := fs.ReadFile(fsys, name)
		if err != nil {
			return MigrationCatalog{}, fmt.Errorf("read migration %s: %w", name, err)
		}
		content := strings.TrimSpace(string(contentBytes))
		if content == "" {
			continue
		}
		nonTx, err := validateMigrationExecutionMode(name, content)
		if err != nil {
			return MigrationCatalog{}, fmt.Errorf("validate migration %s: %w", name, err)
		}
		entries = append(entries, MigrationCatalogEntry{
			Filename:         name,
			Checksum:         migrationContentChecksum(content),
			NonTransactional: nonTx,
		})
	}
	catalog := MigrationCatalog{Entries: entries}
	catalog.SHA256 = digestJSON(struct {
		Entries []MigrationCatalogEntry `json:"entries"`
	}{Entries: entries})
	return catalog, nil
}

// PlanMigrations reads schema_migrations and compares it with the candidate
// filesystem.  The schema_migrations table must already exist; this function
// never writes to the database.
func PlanMigrations(ctx context.Context, db *sql.DB, fsys fs.FS) (MigrationPlan, error) {
	if db == nil {
		return MigrationPlan{}, errors.New("nil sql db")
	}
	rows, err := db.QueryContext(ctx, "SELECT filename, checksum FROM schema_migrations ORDER BY filename")
	if err != nil {
		return MigrationPlan{}, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	records := make([]MigrationRecord, 0)
	for rows.Next() {
		var record MigrationRecord
		if err := rows.Scan(&record.Filename, &record.Checksum); err != nil {
			return MigrationPlan{}, fmt.Errorf("scan schema_migrations: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return MigrationPlan{}, fmt.Errorf("read schema_migrations rows: %w", err)
	}
	return PlanMigrationsFromRecords(fsys, records)
}

// PlanMigrationsFromRecords is the pure planner used by tests and by callers
// that already captured a production schema_migrations snapshot.
func PlanMigrationsFromRecords(fsys fs.FS, records []MigrationRecord) (MigrationPlan, error) {
	catalog, err := BuildMigrationCatalog(fsys)
	if err != nil {
		return MigrationPlan{}, err
	}
	plan := MigrationPlan{
		Catalog:                   catalog,
		CatalogSHA256:             catalog.SHA256,
		ChecksumPolicyVersion:     migrationChecksumPolicyVersion,
		ChecksumPolicySHA256:      MigrationChecksumPolicyDigest(),
		Existing:                  make([]MigrationPlanItem, 0),
		Pending:                   make([]MigrationPlanItem, 0),
		Conflicts:                 make([]MigrationChecksumConflict, 0),
		Unknown:                   make([]MigrationRecord, 0),
		ExistingChecksumsVerified: true,
	}

	dbRecords := make(map[string]string, len(records))
	for _, record := range records {
		if previous, exists := dbRecords[record.Filename]; exists && previous != record.Checksum {
			return plan, fmt.Errorf("duplicate schema_migrations record %s has conflicting checksums", record.Filename)
		}
		dbRecords[record.Filename] = record.Checksum
		if plan.DatabaseHighWatermark == "" || record.Filename > plan.DatabaseHighWatermark {
			plan.DatabaseHighWatermark = record.Filename
		}
	}

	catalogNames := make(map[string]MigrationCatalogEntry, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		catalogNames[entry.Filename] = entry
		stored, ok := dbRecords[entry.Filename]
		if !ok {
			plan.Pending = append(plan.Pending, MigrationPlanItem{
				Filename: entry.Filename, Checksum: entry.Checksum,
				NonTransactional: entry.NonTransactional, Status: "pending",
			})
			continue
		}
		status := "existing"
		if stored != entry.Checksum {
			if !isMigrationChecksumCompatible(entry.Filename, stored, entry.Checksum) {
				plan.Conflicts = append(plan.Conflicts, MigrationChecksumConflict{
					Filename: entry.Filename, DatabaseChecksum: stored, CandidateChecksum: entry.Checksum,
				})
				plan.ExistingChecksumsVerified = false
				continue
			}
			status = "verified-compatible"
		}
		plan.Existing = append(plan.Existing, MigrationPlanItem{
			Filename: entry.Filename, Checksum: entry.Checksum,
			NonTransactional: entry.NonTransactional, Status: status,
		})
	}
	for _, record := range records {
		if _, ok := catalogNames[record.Filename]; !ok {
			plan.Unknown = append(plan.Unknown, record)
		}
	}
	sort.Slice(plan.Unknown, func(i, j int) bool { return plan.Unknown[i].Filename < plan.Unknown[j].Filename })
	if len(plan.Conflicts) > 0 || len(plan.Unknown) > 0 {
		return plan, migrationPlanRejectedError(plan)
	}
	return plan, nil
}

func migrationPlanRejectedError(plan MigrationPlan) error {
	parts := make([]string, 0, 2)
	if len(plan.Conflicts) > 0 {
		parts = append(parts, fmt.Sprintf("%d checksum conflict(s)", len(plan.Conflicts)))
	}
	if len(plan.Unknown) > 0 {
		parts = append(parts, fmt.Sprintf("%d database migration(s) missing from candidate catalog", len(plan.Unknown)))
	}
	return fmt.Errorf("migration plan rejected: %s", strings.Join(parts, "; "))
}

// MigrationPlanJSON returns compact deterministic JSON suitable for signing
// and for shell/VM validators.  It does not include SQL bodies or credentials.
func MigrationPlanJSON(plan MigrationPlan) ([]byte, error) { return json.Marshal(plan) }

// WriteMigrationPlanJSON writes the same deterministic representation to w.
func WriteMigrationPlanJSON(w io.Writer, plan MigrationPlan) error {
	if w == nil {
		return errors.New("nil migration plan writer")
	}
	data, err := MigrationPlanJSON(plan)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// PlanMigrationsJSON is a read-only convenience API for validators.
func PlanMigrationsJSON(ctx context.Context, db *sql.DB, fsys fs.FS) ([]byte, error) {
	plan, err := PlanMigrations(ctx, db, fsys)
	data, marshalErr := MigrationPlanJSON(plan)
	if marshalErr != nil {
		return nil, marshalErr
	}
	if err != nil {
		// Preserve the structured conflicts/unknown records for Gate diagnostics
		// while still returning a non-nil error to force a hard stop.
		return data, err
	}
	return data, nil
}

// ApplyMigrationPlan verifies that the database and candidate still match a
// previously signed read-only plan, then delegates to the existing locked
// executor. Callers must treat any error as a hard stop and reconcile before
// retrying. The final executor re-checks checksums using the same compatibility
// policy and preserves the existing *_notx.sql semantics.
func ApplyMigrationPlan(ctx context.Context, db *sql.DB, fsys fs.FS, expected MigrationPlan) error {
	if db == nil {
		return errors.New("nil sql db")
	}
	if len(expected.Conflicts) > 0 || len(expected.Unknown) > 0 || !expected.ExistingChecksumsVerified {
		return migrationPlanRejectedError(expected)
	}
	actual, err := PlanMigrations(ctx, db, fsys)
	if err != nil {
		return err
	}
	expectedJSON, err := MigrationPlanJSON(expected)
	if err != nil {
		return fmt.Errorf("encode expected migration plan: %w", err)
	}
	actualJSON, err := MigrationPlanJSON(actual)
	if err != nil {
		return fmt.Errorf("encode actual migration plan: %w", err)
	}
	if string(expectedJSON) != string(actualJSON) {
		return errors.New("migration plan drift detected before execution")
	}
	return applyMigrationsFS(ctx, db, fsys)
}

// MigrationChecksumPolicyVersion identifies the compatibility policy shared by
// planner and executor.
func MigrationChecksumPolicyVersion() string { return migrationChecksumPolicyVersion }

// MigrationChecksumPolicyDigest returns a deterministic SHA-256 digest of the
// compatibility whitelist and policy version.
func MigrationChecksumPolicyDigest() string {
	type rule struct {
		Filename          string   `json:"filename"`
		FileChecksum      string   `json:"file_checksum"`
		AcceptedChecksums []string `json:"accepted_checksums"`
	}
	rules := make([]rule, 0, len(migrationChecksumCompatibilityRules))
	for filename, value := range migrationChecksumCompatibilityRules {
		checksums := make([]string, 0, len(value.acceptedChecksums))
		for checksum := range value.acceptedChecksums {
			checksums = append(checksums, checksum)
		}
		sort.Strings(checksums)
		rules = append(rules, rule{Filename: filename, FileChecksum: value.fileChecksum, AcceptedChecksums: checksums})
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Filename < rules[j].Filename })
	return digestJSON(struct {
		Version string `json:"version"`
		Rules   []rule `json:"rules"`
	}{Version: migrationChecksumPolicyVersion, Rules: rules})
}

func digestJSON(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func migrationContentChecksum(content string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(sum[:])
}
