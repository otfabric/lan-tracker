package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/otfabric/lan-tracker/migrations"
)

// TestHostCapMigration upgrades a database at migration 1: the events
// keep their rows and indexes under the new CHECK, which accepts
// HOST_EVICTED and HOST_CAP_REACHED, and hosts gain evicted_at (ADR 0013).
func TestHostCapMigration(t *testing.T) {
	init1, err := migrations.FS.ReadFile("0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hosts.db")
	old := open(t, Options{Path: path, Migrations: fstest.MapFS{"0001_init.sql": {Data: init1}}})
	if err := old.Submit(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		for _, q := range []string{
			`INSERT INTO network_contexts (id, interface, first_seen, last_seen) VALUES (1, 'eth0', 0, 0)`,
			`INSERT INTO hosts (host_id, context_id, mac, presence, first_seen, last_seen) VALUES ('h1', 1, '00:1b:1b:00:00:0a', 'MISSING', 0, 0)`,
			`INSERT INTO events (ts, type, severity, context_id, host_id, cause) VALUES (5, 'HOST_DISCOVERED', 'notice', 1, 'h1', 'passive_arp')`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s := open(t, Options{Path: path})
	if s.SchemaVersion() != LatestSchemaVersion() || LatestSchemaVersion() < 2 {
		t.Fatalf("schema version %d", s.SchemaVersion())
	}
	if err := s.Submit(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		for _, q := range []string{
			`UPDATE hosts SET evicted_at = 10 WHERE host_id = 'h1'`,
			`INSERT INTO events (ts, type, severity, context_id, host_id, cause) VALUES (10, 'HOST_EVICTED', 'notice', 1, 'h1', 'host_cap')`,
			`INSERT INTO events (ts, type, severity, context_id, cause) VALUES (11, 'HOST_CAP_REACHED', 'warning', 1, 'passive_arp')`,
		} {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); err != nil || s.OpErrors() != 0 {
		t.Fatalf("flush: %v, %d writes failed", err, s.OpErrors())
	}
	db := inspect(t, path)
	if n := count(t, db, `SELECT count(*) FROM events WHERE host_id = 'h1'`); n != 2 {
		t.Errorf("events of h1: %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND tbl_name = 'events' AND sql IS NOT NULL`); n != 9 {
		t.Errorf("indexes on events: %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM hosts WHERE evicted_at = 10`); n != 1 {
		t.Errorf("evicted hosts: %d", n)
	}
}
