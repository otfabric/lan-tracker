package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// TestMigrationFailures: a migration set that is not well formed is
// refused before anything runs, and a migration that fails rolls back
// alone: the database keeps the versions before it, and a fixed binary
// continues from there (DATA_MODEL.md §4, AGENTS.md §13).
func TestMigrationFailures(t *testing.T) {
	init1 := &fstest.MapFile{Data: []byte(`CREATE TABLE t (a INTEGER);`)}
	for _, tt := range []struct {
		name string
		fs   fstest.MapFS
		want string
		ran  bool // version 1 was applied before the failure
	}{
		{"version zero", fstest.MapFS{"0000_init.sql": init1}, "NNNN_description.sql", false},
		{"no description", fstest.MapFS{"0001.sql": init1}, "NNNN_description.sql", false},
		{"shared version", fstest.MapFS{"0001_init.sql": init1, "0001_other.sql": init1}, "share version 1", false},
		{"bad SQL", fstest.MapFS{"0001_init.sql": init1, "0002_broken.sql": {Data: []byte(`ALTER TABLE nope ADD COLUMN b INTEGER;`)}},
			"migration 0002_broken.sql: SQL logic error: no such table: nope", true},
		{"records its own version", fstest.MapFS{"0001_init.sql": init1,
			"0002_self.sql": {Data: []byte(`INSERT INTO schema_migrations (version, applied_at) VALUES (2, 0);`)}},
			"migration 0002_self.sql: record version", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hosts.db")
			if _, err := Open(context.Background(), Options{Path: path, Migrations: tt.fs}); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Open: %v", err)
			}
			if !tt.ran {
				return
			}
			// Version 1 stayed; the failed migration left nothing behind.
			db := inspect(t, path)
			if v := count(t, db, `SELECT max(version) FROM schema_migrations`); v != 1 {
				t.Errorf("schema version %d after a failed migration, want 1", v)
			}
			s := open(t, Options{Path: path, Migrations: fstest.MapFS{"0001_init.sql": init1,
				"0002_fixed.sql": {Data: []byte(`ALTER TABLE t ADD COLUMN b INTEGER;`)}}})
			if s.SchemaVersion() != 2 {
				t.Errorf("fixed binary: schema version %d", s.SchemaVersion())
			}
		})
	}
}
