package database

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestPostgres107Fingerprint(t *testing.T) {
	tests := []struct {
		name      string
		values    []bool // Octo scopes, bindings, connections; upstream markers
		wantError string
	}{
		{"legacy fork", []bool{true, true, true, false, false, false, false, false, false}, "fork's legacy numbering"},
		{
			"partial octo fingerprint",
			[]bool{false, true, false, true, false, false, true, true, true},
			"fork's legacy numbering",
		},
		{"normal upstream 107", []bool{false, false, false, true, true, true, true, true, true}, ""},
		{
			"missing upstream object",
			[]bool{false, false, false, true, true, false, true, true, true},
			"ambiguous or incomplete",
		},
		{
			"missing fork schema",
			[]bool{false, false, false, true, true, true, true, false, true},
			"ambiguous or incomplete",
		},
		{
			"missing skill or valid index",
			[]bool{false, false, false, true, true, true, true, true, false},
			"ambiguous or incomplete",
		},
		{"mixed histories", []bool{true, true, true, true, true, true, true, true, true}, "ambiguous or incomplete"},
		{
			"empty schema",
			[]bool{false, false, false, false, false, false, false, false, false},
			"ambiguous or incomplete",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			rows := sqlmock.NewRows([]string{
				"scopes", "bindings", "connections", "mcp", "artifacts", "deleted",
				"checkpoint", "fork", "skill_index",
			})
			rows.AddRow(tt.values[0], tt.values[1], tt.values[2], tt.values[3], tt.values[4], tt.values[5],
				tt.values[6], tt.values[7], tt.values[8])
			mock.ExpectQuery(regexp.QuoteMeta(postgres107FingerprintSQL)).WillReturnRows(rows)
			err = checkPostgres107Fingerprint(context.Background(), db)
			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantError)
				require.ErrorContains(t, err, "isolated, consistent backup")
				require.ErrorContains(t, err, "do not force or rewind")
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPostgres107InspectionFailureRejects(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(regexp.QuoteMeta(postgres107FingerprintSQL)).WillReturnError(errors.New("inspection failed"))
	err = checkPostgres107Fingerprint(context.Background(), db)
	require.ErrorContains(t, err, "cannot inspect")
	require.ErrorContains(t, err, "controlled recovery")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPostgres107DirtyRejectsBeforeConnecting(t *testing.T) {
	// The invalid DSN proves the dirty guard returns without connecting.
	err := checkPostgres107BeforeMigration(context.Background(), "://invalid", true)
	require.ErrorContains(t, err, "107 is dirty")
	require.ErrorContains(t, err, "do not force or rewind")
}

func TestPostgresLegacyEarlyVersionBoundaries(t *testing.T) {
	for _, tt := range []struct {
		version uint
		want    bool
	}{
		{0, false},
		{95, false},
		{96, true},
		{97, true},
		{105, true},
		{106, true},
		{107, false},
		{108, false},
		{130, false},
	} {
		require.Equal(t, tt.want, isPostgresLegacyEarlyVersion(tt.version), "version %d", tt.version)
	}
}

func TestPostgresLegacyEarlyFingerprint(t *testing.T) {
	for _, version := range []uint{96, 97, 100, 105, 106} {
		for _, exists := range []bool{false, true} {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			mock.ExpectQuery(regexp.QuoteMeta(postgresLegacyEarlyFingerprintSQL)).
				WillReturnRows(sqlmock.NewRows([]string{"fork_fingerprint"}).AddRow(exists))
			err = checkPostgresLegacyEarlyFingerprint(context.Background(), db, version)
			if exists {
				require.ErrorContains(t, err, "fork Octo schema fingerprint")
				require.ErrorContains(t, err, "do not force or rewind")
			} else {
				require.NoError(t, err)
			}
			mock.ExpectClose()
			require.NoError(t, db.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		}
	}
}

func TestPostgresLegacyEarlyInspectionFailureRejects(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(regexp.QuoteMeta(postgresLegacyEarlyFingerprintSQL)).
		WillReturnError(errors.New("inspection failed"))
	err = checkPostgresLegacyEarlyFingerprint(context.Background(), db, 96)
	require.ErrorContains(t, err, "cannot inspect PostgreSQL migration 96")
	require.ErrorContains(t, err, "controlled recovery")
	require.NoError(t, mock.ExpectationsWereMet())
}
