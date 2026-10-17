package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/otfabric/lan-tracker/internal/store"
	"github.com/otfabric/lan-tracker/migrations"
)

func exec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	ctx := context.Background()
	if err := st.Submit(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if st.OpErrors() != 0 {
		t.Fatalf("write failed: %s", q)
	}
}

func TestLastScan(t *testing.T) {
	st, _, _ := seed(t)
	r := reader(t, st)
	ctx := context.Background()
	if s, err := r.LastScan(ctx); err != nil || s != nil {
		t.Fatalf("LastScan without scans = %+v, %v", s, err)
	}
	ms := t0.UnixMilli()
	exec(t, st, `INSERT INTO scans (id, context_id, kind, trigger, started_at, finished_at, targets, results_json)
		VALUES (1, 1, 'arp', 'operator', ?, ?, 253, '{"responders":3}')`, ms, ms+31000)
	s, err := r.LastScan(ctx)
	if err != nil || s == nil || s.ID != 1 || s.Kind != "arp" || s.Trigger != "operator" || s.Targets != 253 ||
		!s.Started.Equal(t0) || s.Finished == nil || s.Finished.Sub(t0).Seconds() != 31 || string(s.Results) != `{"responders":3}` {
		t.Fatalf("LastScan = %+v, %v", s, err)
	}
	exec(t, st, `INSERT INTO scans (id, context_id, kind, trigger, started_at, targets) VALUES (2, 1, 'arp,tcp/502', 'operator', ?, 10)`, ms+60000)
	if s, err := r.LastScan(ctx); err != nil || s.ID != 2 || s.Finished != nil || s.Interface == "" {
		t.Fatalf("running scan = %+v, %v", s, err)
	}
}

func TestKnownIPv4(t *testing.T) {
	st, _, _ := seed(t)
	r := reader(t, st)
	ctx := context.Background()
	got, err := r.KnownIPv4(ctx, "eth1")
	if err != nil || len(got) == 0 {
		t.Fatalf("KnownIPv4 = %v, %v", got, err)
	}
	var want int
	if err := st.View(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(DISTINCT ip) FROM addresses WHERE family = 4 AND ended_at IS NULL
			AND context_id = (SELECT id FROM network_contexts WHERE interface = 'eth1')`).Scan(&want)
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != want {
		t.Errorf("%d known addresses, want %d", len(got), want)
	}
	for _, a := range got {
		if !a.IP.Is4() || a.LastSeen.IsZero() {
			t.Errorf("known address %+v", a)
		}
	}
	if none, err := r.KnownIPv4(ctx, "eth9"); err != nil || len(none) != 0 {
		t.Errorf("unknown interface: %v, %v", none, err)
	}
}

// LAN Sentinel's identification rows stay readable as legacy history; the
// read model no longer has a device type or manufacturer (ADR 0012).
func TestLegacyIdentificationsReadable(t *testing.T) {
	st, _, _ := seed(t)
	r := reader(t, st)
	ctx := context.Background()
	hosts, err := r.Hosts(ctx, store.HostFilter{})
	if err != nil || len(hosts) == 0 {
		t.Fatalf("hosts = %v, %v", hosts, err)
	}
	id := hosts[0].HostID
	exec(t, st, `INSERT INTO identifications (host_id, field, value, confidence, source, evidence_json, first_seen, last_seen, current)
		VALUES (?, 'device_type', 'PLC', 0.9, 'modbus', '{}', ?, ?, 1)`, id, t0.UnixMilli(), t0.UnixMilli())
	exec(t, st, `UPDATE hosts SET device_type = 'PLC', manufacturer = 'ACME' WHERE host_id = ?`, id)
	h, err := r.Host(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, i := range h.LegacyIdentifications {
		found = found || i.Source == "modbus" && i.Value == "PLC"
	}
	if !found {
		t.Errorf("legacy identifications = %+v", h.LegacyIdentifications)
	}
	b, _ := json.Marshal(h)
	if strings.Contains(string(b), `"device_type":`) || strings.Contains(string(b), `"manufacturer":`) {
		t.Errorf("the read model still reports identification: %s", b)
	}
}

func TestOfflineRefusesOtherSchemaVersions(t *testing.T) {
	sub := func(names ...string) fstest.MapFS {
		m := fstest.MapFS{}
		for _, n := range names {
			b, err := fs.ReadFile(migrations.FS, n)
			if err != nil {
				t.Fatal(err)
			}
			m[n] = &fstest.MapFile{Data: b}
		}
		return m
	}
	all, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	older := sub(all[:len(all)-1]...)
	newer := sub(all...)
	newer["9999_future.sql"] = &fstest.MapFile{Data: []byte(`CREATE TABLE future (a INTEGER);`)}
	for _, tt := range []struct {
		name string
		fs   fs.FS
		want string
	}{
		{"older", older, "start the daemon once"},
		{"newer", newer, "newer than this binary"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hosts.db")
			st, err := store.Open(context.Background(), store.Options{Path: path, Migrations: tt.fs})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = store.OpenReadOnly(path, store.ReadOptions{})
			if !errors.Is(err, store.ErrSchemaVersion) || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("OpenReadOnly = %v", err)
			}
		})
	}
}

// Events written before migration 0004 recorded a synchronised clock
// without checking it: they become unknown, and the old column is gone.
func TestClockSyncMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.db")
	all, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, n := range all {
		if n >= "0004" {
			continue
		}
		b, _ := fs.ReadFile(migrations.FS, n)
		old[n] = &fstest.MapFile{Data: b}
	}
	st, err := store.Open(context.Background(), store.Options{Path: path, Migrations: old})
	if err != nil {
		t.Fatal(err)
	}
	exec(t, st, `INSERT INTO events (ts, type, severity, cause, clock_synced) VALUES (0, 'INTERFACE_UP', 'notice', 'test', 1)`)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(context.Background(), store.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var clock string
	var oldCols int
	if err := st.View(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT clock_sync FROM events`).Scan(&clock); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('events') WHERE name = 'clock_synced'`).Scan(&oldCols)
	}); err != nil {
		t.Fatal(err)
	}
	if clock != "unknown" || oldCols != 0 {
		t.Errorf("after migration: clock_sync %q, clock_synced columns %d", clock, oldCols)
	}
	ctx := context.Background()
	if err := st.Submit(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO events (ts, type, severity, cause, clock_sync) VALUES (0, 'INTERFACE_UP', 'notice', 'test', 'maybe')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Flush(ctx)
	if st.OpErrors() != 1 {
		t.Error("a clock_sync outside synced/unsynced/unknown was accepted")
	}
}

// Name queries ask only about what is known, and not about what a record's
// TTL still covers.
func TestNameTargets(t *testing.T) {
	st, _, _ := seed(t)
	r := reader(t, st)
	ctx := context.Background()
	now := t0.Add(5 * time.Hour)
	exec(t, st, `INSERT INTO name_records (context_id, name, name_type, record_type, address, source, link, first_seen, last_seen, expires_at)
		SELECT id, 'fresh.local', 'mdns', 'A', '192.168.110.60', 'passive_mdns', 'none', ?, ?, ? FROM network_contexts WHERE interface = 'eth1'
		UNION ALL SELECT id, 'plc.plant.example', 'dns', 'A', '192.168.110.50', 'passive_dns', 'none', ?, ?, ? FROM network_contexts WHERE interface = 'eth1'
		UNION ALL SELECT id, 'b.plant.example', 'dns_ptr', 'PTR', '192.168.110.50', 'passive_dns', 'none', ?, ?, ? FROM network_contexts WHERE interface = 'eth1'`,
		now.UnixMilli(), now.UnixMilli(), now.Add(time.Minute).UnixMilli(),
		t0.UnixMilli(), t0.UnixMilli(), t0.Add(time.Minute).UnixMilli(),
		now.UnixMilli(), now.UnixMilli(), now.Add(time.Hour).UnixMilli())
	got, err := r.NameTargets(ctx, "eth1", now, 10)
	if err != nil {
		t.Fatal(err)
	}
	// plc-b.local has no TTL (the seed's older form): asked; fresh.local's
	// TTL still covers it. .50 has a PTR in effect; .51 and .52 do not.
	if !slices.Equal(got.MDNS, []string{"plc-b.local"}) || !slices.Equal(got.DNS, []string{"plc.plant.example"}) ||
		len(got.PTR) != 2 || got.PTR[0].String() != "192.168.110.51" {
		t.Errorf("targets = %+v", got)
	}
	if got, _ := r.NameTargets(ctx, "eth1", now, 1); len(got.PTR) != 1 {
		t.Errorf("limit: %+v", got)
	}
}
