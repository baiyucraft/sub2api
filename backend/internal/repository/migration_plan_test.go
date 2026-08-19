package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestBuildMigrationCatalog_SortsAndNormalizesChecksums(t *testing.T) {
	fsys := fstest.MapFS{
		"002_second.sql": &fstest.MapFile{Data: []byte("\n CREATE TABLE second (id int); \n")},
		"001_first.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE first (id int);\n")},
		"000_empty.sql":  &fstest.MapFile{Data: []byte(" \n\t")},
	}
	catalog, err := BuildMigrationCatalog(fsys)
	require.NoError(t, err)
	require.Equal(t, []string{"001_first.sql", "002_second.sql"}, []string{
		catalog.Entries[0].Filename, catalog.Entries[1].Filename,
	})
	require.Equal(t, migrationContentChecksum("CREATE TABLE first (id int);"), catalog.Entries[0].Checksum)
	require.Equal(t, migrationContentChecksum("CREATE TABLE second (id int);"), catalog.Entries[1].Checksum)
	require.Len(t, catalog.SHA256, 64)
}

func TestBuildMigrationCatalog_RejectsInvalidNotx(t *testing.T) {
	fsys := fstest.MapFS{
		"001_bad_notx.sql": &fstest.MapFile{Data: []byte("CREATE INDEX CONCURRENTLY idx_a ON t(a);")},
	}
	_, err := BuildMigrationCatalog(fsys)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validate migration 001_bad_notx.sql")
}

func TestPlanMigrationsFromRecords_PendingExistingAndCompatible(t *testing.T) {
	fsys := fstest.MapFS{
		"001_first.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE first (id int);")},
		"002_second.sql": &fstest.MapFile{Data: []byte("CREATE TABLE second (id int);")},
	}
	firstChecksum := migrationContentChecksum("CREATE TABLE first (id int);")
	plan, err := PlanMigrationsFromRecords(fsys, []MigrationRecord{{Filename: "001_first.sql", Checksum: firstChecksum}})
	require.NoError(t, err)
	require.True(t, plan.ExistingChecksumsVerified)
	require.Equal(t, []string{"001_first.sql"}, []string{plan.Existing[0].Filename})
	require.Equal(t, "existing", plan.Existing[0].Status)
	require.Equal(t, []string{"002_second.sql"}, []string{plan.Pending[0].Filename})
	require.Empty(t, plan.Conflicts)
	require.Empty(t, plan.Unknown)
	require.Equal(t, "001_first.sql", plan.DatabaseHighWatermark)
}

func TestPlanMigrationsFromRecords_CompatibilityAndHardStops(t *testing.T) {
	name := "054_drop_legacy_cache_columns.sql"
	entry := migrationChecksumCompatibilityRules[name]
	fsys := fstest.MapFS{
		name: &fstest.MapFile{Data: []byte("SELECT 1;")},
	}
	// The fixture content is not the historical checksum; use a synthetic
	// compatibility rule to prove the planner shares the production policy.
	original := migrationChecksumCompatibilityRules[name]
	migrationChecksumCompatibilityRules[name] = newMigrationChecksumCompatibilityRule(
		migrationContentChecksum("SELECT 1;"), entry.fileChecksum,
	)
	t.Cleanup(func() { migrationChecksumCompatibilityRules[name] = original })
	plan, err := PlanMigrationsFromRecords(fsys, []MigrationRecord{{Filename: name, Checksum: entry.fileChecksum}})
	require.NoError(t, err)
	require.Equal(t, "verified-compatible", plan.Existing[0].Status)

	_, err = PlanMigrationsFromRecords(fsys, []MigrationRecord{{Filename: name, Checksum: "not-a-valid-checksum"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "checksum conflict")

	_, err = PlanMigrationsFromRecords(fsys, []MigrationRecord{{Filename: "999_removed.sql", Checksum: "abc"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing from candidate catalog")
}

func TestPlanMigrations_ReadOnlyJSON(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery("SELECT filename, checksum FROM schema_migrations ORDER BY filename").
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}))
	fsys := fstest.MapFS{"001_init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")}}
	data, err := PlanMigrationsJSON(context.Background(), db, fsys)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
	var decoded MigrationPlan
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.Pending, 1)
	require.Equal(t, MigrationChecksumPolicyVersion(), decoded.ChecksumPolicyVersion)
	require.Len(t, decoded.ChecksumPolicySHA256, 64)
	require.True(t, strings.Contains(string(data), `"catalog_sha256"`))
}

func TestPlanMigrationsJSON_PreservesRejectedPlan(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery("SELECT filename, checksum FROM schema_migrations ORDER BY filename").
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}).AddRow("001_init.sql", "changed"))
	fsys := fstest.MapFS{"001_init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")}}
	data, err := PlanMigrationsJSON(context.Background(), db, fsys)
	require.Error(t, err)
	require.NotEmpty(t, data)
	require.NoError(t, mock.ExpectationsWereMet())
	var decoded MigrationPlan
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.Conflicts, 1)
}

func TestMigrationChecksumPolicyDigestStable(t *testing.T) {
	first := MigrationChecksumPolicyDigest()
	second := MigrationChecksumPolicyDigest()
	require.Len(t, first, 64)
	require.Equal(t, first, second)
}

func TestApplyMigrationPlan_RejectsExpectedConflictWithoutDatabaseAccess(t *testing.T) {
	plan := MigrationPlan{
		Conflicts: []MigrationChecksumConflict{{Filename: "001_init.sql"}},
	}
	err := ApplyMigrationPlan(context.Background(), &sql.DB{}, fstest.MapFS{}, plan)
	require.Error(t, err)
	require.Contains(t, err.Error(), "migration plan rejected")
}

func TestApplyMigrationPlan_RejectsSnapshotDrift(t *testing.T) {
	fsys := fstest.MapFS{"001_init.sql": &fstest.MapFile{Data: []byte("SELECT 1;")}}
	expected, err := PlanMigrationsFromRecords(fsys, nil)
	require.NoError(t, err)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery("SELECT filename, checksum FROM schema_migrations ORDER BY filename").
		WillReturnRows(sqlmock.NewRows([]string{"filename", "checksum"}).AddRow("001_init.sql", "different"))
	err = ApplyMigrationPlan(context.Background(), db, fsys, expected)
	require.Error(t, err)
	require.Contains(t, err.Error(), "checksum conflict")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlanMigrations_NilDB(t *testing.T) {
	_, err := PlanMigrations(context.Background(), (*sql.DB)(nil), fstest.MapFS{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil sql db")
}
