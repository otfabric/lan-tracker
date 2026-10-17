package store

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/clock"
)

const (
	hour = time.Hour
	day  = 24 * time.Hour
)

var defaultRetention = Retention{Observations: 7 * day, Rollups: 90 * day, Events: 730 * day}

// seed writes one context and host, then calls fn inside one op.
func seed(t *testing.T, s *Store, fn func(ctx context.Context, tx *sql.Tx) error) {
	t.Helper()
	err := s.Submit(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO network_contexts (id, interface, first_seen, last_seen) VALUES (1, 'eth1', 0, 0)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO hosts (host_id, context_id, mac, presence, first_seen, last_seen)
			VALUES ('h1', 1, '00:1b:1b:00:00:01', 'ACTIVE', 0, 0)`); err != nil {
			return err
		}
		return fn(ctx, tx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(context.Background()); err != nil || s.OpErrors() != 0 {
		t.Fatalf("seed: %v, %d op errors", err, s.OpErrors())
	}
}

func addObservations(n int, at func(i int) time.Time, host, ip string, meta string) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		for i := range n {
			var h, m any
			if host != "" {
				h = host
			}
			if meta != "" {
				m = meta
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO observations (ts, context_id, source, mac, ip, meta_json, host_id)
				VALUES (?, 1, 'passive_arp', '00:1b:1b:00:00:01', ?, ?, ?)`, at(i).UnixMilli(), ip, m, h); err != nil {
				return err
			}
		}
		return nil
	}
}

func addEvents(n int, at func(i int) time.Time) func(ctx context.Context, tx *sql.Tx) error {
	return func(ctx context.Context, tx *sql.Tx) error {
		for i := range n {
			if _, err := tx.ExecContext(ctx, `INSERT INTO events (ts, type, severity, context_id, host_id, cause, clock_sync)
				VALUES (?, 'IP_ADDED', 'notice', 1, 'h1', 'passive_arp', 'synced')`, at(i).UnixMilli()); err != nil {
				return err
			}
		}
		return nil
	}
}

func TestCompactRetention(t *testing.T) {
	s := open(t, Options{})
	now := t0.Add(100 * day)
	seed(t, s, func(ctx context.Context, tx *sql.Tx) error {
		// 3 observations at 10:05-10:25 eight days ago (one hour bucket), one
		// unbound at 11:00 the same day, and 2 recent ones.
		old := now.Add(-8 * day).Truncate(hour)
		for _, f := range []func(context.Context, *sql.Tx) error{
			addObservations(3, func(i int) time.Time { return old.Add(time.Duration(5+10*i) * time.Minute) }, "h1", "10.0.0.1", `{"arp":"reply"}`),
			addObservations(1, func(int) time.Time { return old.Add(hour) }, "", "10.0.0.9", ""),
			addObservations(2, func(i int) time.Time { return now.Add(-time.Duration(i+1) * hour) }, "h1", "10.0.0.1", ""),
			addEvents(1, func(int) time.Time { return now.Add(-800 * day) }),
			addEvents(1, func(int) time.Time { return now.Add(-day) }),
		} {
			if err := f(ctx, tx); err != nil {
				return err
			}
		}
		// History as old as the pruned event: an endpoint's old result
		// change and a name that ended; the endpoint itself and an open
		// name stay.
		ancient, recent := now.Add(-800*day).UnixMilli(), now.Add(-day).UnixMilli()
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO endpoints (id, context_id, ip, proto, port, last_result, last_result_at, first_seen) VALUES (1, 1, '10.0.0.1', 'tcp', 502, 'OPEN', ?, ?)`,
				[]any{recent, ancient}},
			{`INSERT INTO endpoint_results (endpoint_id, ts, result) VALUES (1, ?, 'REFUSED'), (1, ?, 'OPEN')`, []any{ancient, recent}},
			{`INSERT INTO name_records (context_id, name, name_type, record_type, source, link, first_seen, last_seen, ended_at)
				VALUES (1, 'old.local', 'mdns', 'A', 'passive_mdns', 'none', ?, ?, ?), (1, 'open.local', 'mdns', 'A', 'passive_mdns', 'none', ?, ?, NULL)`,
				[]any{ancient, ancient, ancient, ancient, ancient}},
			// A lease that ended goes with the events; an open one stays
			// however old. Exchanges go with the roll-ups.
			{`INSERT INTO dhcp_leases (context_id, chaddr, ip, server_id, sender_mac, sender_ip, relay, first_ack, last_ack, ended_at, end_reason)
				VALUES (1, '00:1b:1b:00:00:01', '10.0.0.5', '10.0.0.2', '', '', '', ?, ?, ?, 'superseded'),
				(1, '00:1b:1b:00:00:01', '10.0.0.6', '10.0.0.2', '', '', '', ?, ?, NULL, NULL)`,
				[]any{ancient, ancient, ancient, ancient, ancient}},
			{`INSERT INTO dhcp_exchanges (context_id, xid, chaddr, first_seen, last_seen, messages, outcome, visibility)
				VALUES (1, 1, '00:1b:1b:00:00:01', ?, ?, 'ack', 'ack', 'partial'), (1, 2, '00:1b:1b:00:00:01', ?, ?, 'ack', 'ack', 'partial')`,
				[]any{ancient, ancient, recent, recent}},
		} {
			if _, err := tx.ExecContext(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO observation_rollups (hour, context_id, host_id, source, mac, ip, count, first_ts, last_ts)
			VALUES (?, 1, 'h1', 'passive_arp', NULL, '10.0.0.1', 5, ?, ?)`, now.Add(-95*day).UnixMilli(), now.Add(-95*day).UnixMilli(), now.Add(-95*day).UnixMilli())
		return err
	})
	res, err := s.Compact(context.Background(), defaultRetention, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.RolledUp != 4 || res.RollupsPruned != 1 || res.EventsPruned != 1 || res.HistoryPruned != 4 {
		t.Errorf("result = %+v", res)
	}
	if got := strings.Join(queryStrings(t, inspect(t, s.Path()), `SELECT name FROM name_records UNION ALL SELECT result FROM endpoint_results
		UNION ALL SELECT last_result FROM endpoints UNION ALL SELECT ip FROM dhcp_leases UNION ALL SELECT xid FROM dhcp_exchanges`), " "); got != "open.local OPEN OPEN 10.0.0.6 2" {
		t.Errorf("history left = %s", got)
	}
	db := inspect(t, s.Path())
	if n := count(t, db, `SELECT count(*) FROM observations`); n != 2 {
		t.Errorf("%d raw observations left, want the 2 recent ones", n)
	}
	rows := strings.Join(queryStrings(t, db, `SELECT coalesce(host_id, '-') || ' ' || ip || ' ' || count || ' ' || ((last_ts - first_ts) / 60000)
		FROM observation_rollups ORDER BY hour`), "|")
	if rows != "h1 10.0.0.1 3 20|- 10.0.0.9 1 0" {
		t.Errorf("roll-ups = %s", rows)
	}
	if n := count(t, db, `SELECT count(*) FROM events`); n != 1 {
		t.Errorf("%d events left, want 1", n)
	}

	// Rolling up again into the same hour adds to the existing row.
	seed(t, s, addObservations(2, func(i int) time.Time { return now.Add(-8 * day).Truncate(hour).Add(50 * time.Minute) }, "h1", "10.0.0.1", ""))
	if _, err := s.Compact(context.Background(), defaultRetention, now); err != nil {
		t.Fatal(err)
	}
	if got := count(t, db, `SELECT count FROM observation_rollups WHERE host_id = 'h1'`); got != 5 {
		t.Errorf("merged roll-up count = %d, want 5", got)
	}
}

func TestCompactBatches(t *testing.T) {
	s := open(t, Options{})
	n := 2*CompactBatch + 17
	seed(t, s, addObservations(n, func(i int) time.Time { return t0.Add(time.Duration(i) * time.Second) }, "h1", "10.0.0.1", ""))
	commits := s.Commits()
	res, err := s.Compact(context.Background(), defaultRetention, t0.Add(30*day))
	if err != nil {
		t.Fatal(err)
	}
	if res.RolledUp != int64(n) {
		t.Errorf("rolled up %d, want %d", res.RolledUp, n)
	}
	if s.Commits()-commits < 3 {
		t.Errorf("%d commits for %d rows: compaction did not batch", s.Commits()-commits, n)
	}
	db := inspect(t, s.Path())
	if got := count(t, db, `SELECT sum(count) FROM observation_rollups`); got != n {
		t.Errorf("roll-ups count %d observations, want %d", got, n)
	}
}

func TestCompactSizeCap(t *testing.T) {
	s := open(t, Options{})
	meta := `{"pad":"` + strings.Repeat("x", 400) + `"}`
	// Recent observations (within retention) and events: only the cap can
	// remove them.
	now := t0.Add(time.Hour)
	seed(t, s, addObservations(6000, func(i int) time.Time { return t0.Add(time.Duration(i) * 100 * time.Millisecond) }, "h1", "10.0.0.1", meta))
	seed(t, s, addEvents(50, func(i int) time.Time { return t0.Add(time.Duration(i) * time.Second) }))
	// Checkpoint so the file holds the data before measuring it.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fileBefore := fileSize(t, s.Path())
	s = open(t, Options{Path: s.Path()})
	before, err := s.UsedSize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	limit := before / 2
	r := defaultRetention
	r.MaxDBSize = limit
	res, err := s.Compact(context.Background(), r, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.SizeAfter > limit || res.RolledUp == 0 || res.EventsPruned != 0 {
		t.Errorf("result = %+v (limit %d): want observations rolled up below the cap and events kept", res, limit)
	}
	// Freed pages are returned to the file system once checkpointed.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if after := fileSize(t, s.Path()); after >= fileBefore {
		t.Errorf("file did not shrink: %d -> %d bytes", fileBefore, after)
	}

	// A cap below what roll-ups and events need prunes those too, oldest
	// first.
	s = open(t, Options{Path: s.Path()})
	r.MaxDBSize = 1
	res, err = s.Compact(context.Background(), r, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.RollupsPruned == 0 || res.EventsPruned != 50 {
		t.Errorf("tiny cap: %+v", res)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

func TestAutoVacuumOnNewDatabase(t *testing.T) {
	s := open(t, Options{})
	db := inspect(t, s.Path())
	if mode := count(t, db, `PRAGMA auto_vacuum`); mode != 2 {
		t.Errorf("auto_vacuum = %d, want 2 (incremental)", mode)
	}
}

func TestRunCompaction(t *testing.T) {
	sim := clock.NewSim(t0.Add(30 * day))
	s := open(t, Options{Clock: sim})
	seed(t, s, addObservations(1, func(int) time.Time { return t0 }, "h1", "10.0.0.1", ""))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	runs := 0
	go func() {
		defer close(done)
		s.RunCompaction(ctx, sim, hour, func() Retention { runs++; return defaultRetention }, slog.New(slog.DiscardHandler))
	}()
	db := inspect(t, s.Path())
	waitFor(t, func() bool { return count(t, db, `SELECT count(*) FROM observations`) == 0 })
	seed(t, s, addObservations(1, func(int) time.Time { return t0 }, "h1", "10.0.0.1", ""))
	waitFor(t, func() bool { return sim.Waiters() > 0 })
	sim.Advance(hour)
	waitFor(t, func() bool { return count(t, db, `SELECT count(*) FROM observations`) == 0 })
	cancel()
	<-done
	if runs < 2 {
		t.Errorf("%d runs, want at least 2", runs)
	}
}

func queryStrings(t *testing.T, db *sql.DB, q string) []string {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
