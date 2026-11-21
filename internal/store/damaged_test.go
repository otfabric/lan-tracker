package store_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/store"
	"github.com/otfabric/lan-tracker/internal/store/storetest"
)

// errOf keeps a query's error.
func errOf[T any](_ T, err error) error { return err }

// readCalls runs every Reader query with arguments that reach rows of the
// golden scenario, so each of its follow-up queries runs too.
func readCalls(r *store.Reader, hostID string) map[string]func(context.Context) error {
	at, later := t0.Add(3*time.Hour), t0.Add(5*time.Hour)
	since, until := t0, t0.Add(10*time.Hour)
	return map[string]func(context.Context) error{
		"hosts":      func(ctx context.Context) error { return errOf(r.Hosts(ctx, store.HostFilter{})) },
		"hosts port": func(ctx context.Context) error { return errOf(r.Hosts(ctx, store.HostFilter{Port: 502, Live: true})) },
		"host":       func(ctx context.Context) error { return errOf(r.Host(ctx, hostID)) },
		"find ip":    func(ctx context.Context) error { return errOf(r.Find(ctx, store.FindQuery{Query: "192.168.110.50"})) },
		"find ip at": func(ctx context.Context) error {
			return errOf(r.Find(ctx, store.FindQuery{Query: "192.168.110.50", At: &at}))
		},
		"find mac": func(ctx context.Context) error {
			return errOf(r.Find(ctx, store.FindQuery{Query: "00:1b:1b:aa:bb:01"}))
		},
		"find mac at": func(ctx context.Context) error {
			return errOf(r.Find(ctx, store.FindQuery{Query: "00:1b:1b:aa:bb:01", At: &at}))
		},
		"find name": func(ctx context.Context) error {
			return errOf(r.Find(ctx, store.FindQuery{Query: "plc", Kind: store.KindName}))
		},
		"history": func(ctx context.Context) error {
			return errOf(r.History(ctx, store.HistoryQuery{Query: "192.168.110.50"}))
		},
		"history iface": func(ctx context.Context) error {
			return errOf(r.History(ctx, store.HistoryQuery{Query: "plc-b.local", Kind: store.KindName, Interface: "eth1"}))
		},
		"find name iface": func(ctx context.Context) error {
			return errOf(r.Find(ctx, store.FindQuery{Query: "plc-b.local", Kind: store.KindName, Interface: "eth1", At: &later}))
		},
		"events":       func(ctx context.Context) error { return errOf(r.Events(ctx, store.EventFilter{})) },
		"events ip":    func(ctx context.Context) error { return errOf(r.Events(ctx, store.EventFilter{IP: "192.168.110.50"})) },
		"observations": func(ctx context.Context) error { return errOf(r.Observations(ctx, store.ObservationFilter{})) },
		"rollups":      func(ctx context.Context) error { return errOf(r.Rollups(ctx, store.ObservationFilter{})) },
		"services":     func(ctx context.Context) error { return errOf(r.Services(ctx, store.ServiceFilter{})) },
		"services at":  func(ctx context.Context) error { return errOf(r.Services(ctx, store.ServiceFilter{At: &later})) },
		"interfaces":   func(ctx context.Context) error { return errOf(r.Interfaces(ctx)) },
		"db info":      func(ctx context.Context) error { return errOf(r.DBInfo(ctx)) },
		"db summary":   func(ctx context.Context) error { return errOf(r.DBSummary(ctx)) },
		"evidence":     func(ctx context.Context) error { return errOf(r.Evidence(ctx, hostID, time.Time{})) },
		"evidence for": func(ctx context.Context) error {
			return errOf(r.EvidenceFor(ctx, store.HostFilter{Interface: "eth1"}, time.Time{}))
		},
		"active state":  func(ctx context.Context) error { return errOf(r.ActiveState(ctx)) },
		"last scan":     func(ctx context.Context) error { return errOf(r.LastScan(ctx)) },
		"known ipv4":    func(ctx context.Context) error { return errOf(r.KnownIPv4(ctx, "eth1")) },
		"name targets":  func(ctx context.Context) error { return errOf(r.NameTargets(ctx, "eth1", until, 100)) },
		"dhcp servers":  func(ctx context.Context) error { return errOf(r.DHCPServers(ctx, store.DHCPServerFilter{})) },
		"dhcp counts":   func(ctx context.Context) error { return errOf(r.DHCPServerCounts(ctx, time.Time{})) },
		"dhcp leases":   func(ctx context.Context) error { return errOf(r.DHCPLeases(ctx, store.DHCPLeaseFilter{})) },
		"dhcp exchange": func(ctx context.Context) error { return errOf(r.DHCPExchanges(ctx, store.DHCPExchangeFilter{})) },
		"host counts":   func(ctx context.Context) error { return errOf(r.HostCounts(ctx)) },
		"activity": func(ctx context.Context) error {
			return errOf(r.Activity(ctx, store.ActivityFilter{Since: since, Until: until}))
		},
		"activity host": func(ctx context.Context) error {
			return errOf(r.Activity(ctx, store.ActivityFilter{HostID: hostID, Interface: "eth1", Since: since, Until: until}))
		},
	}
}

// TestReadsFailOnDamagedSchema takes one table away at a time, as a
// damaged or foreign database would: every read that needs the table fails
// with SQLite's error, wrapped with the database path, and never answers
// from the tables that are left. With the table back, every read works.
func TestReadsFailOnDamagedSchema(t *testing.T) {
	st, path, exp := seed(t)
	storetest.AddDHCPServers(t, st)
	storetest.AddDHCPLeases(t, st)
	r := reader(t, st)
	ctx := context.Background()
	hosts, err := r.Hosts(ctx, store.HostFilter{Query: exp.Hosts["A"]})
	if err != nil || len(hosts) != 1 {
		t.Fatalf("host A: %v %+v", err, hosts)
	}
	calls := readCalls(r, hosts[0].HostID)

	for _, table := range []string{
		"network_contexts", "context_prefixes", "hosts", "addresses", "address_sources", "name_records",
		"endpoints", "endpoint_results", "events", "observations", "observation_rollups",
		"dhcp_servers", "dhcp_leases", "dhcp_exchanges", "scans", "runtime_state",
	} {
		t.Run(table, func(t *testing.T) {
			exec(t, st, `ALTER TABLE `+table+` RENAME TO damaged`)
			failed := 0
			for name, call := range calls {
				err := call(ctx)
				if err == nil {
					continue
				}
				failed++
				if !strings.Contains(err.Error(), "no such table: "+table) || !strings.Contains(err.Error(), path) {
					t.Errorf("%s: %v", name, err)
				}
			}
			exec(t, st, `ALTER TABLE damaged RENAME TO `+table)
			if failed == 0 {
				t.Errorf("no read needs %s", table)
			}
			for name, call := range calls {
				if err := call(ctx); err != nil {
					t.Errorf("%s with %s back: %v", name, table, err)
				}
			}
		})
	}
}

// TestReadsFailOnForeignValues puts a value of the wrong type in one column
// at a time (a view in place of the table, as a foreign writer could leave
// it): a read that scans the column fails with the database path rather
// than answering with a zero value, and the reads of every table notice.
func TestReadsFailOnForeignValues(t *testing.T) {
	st, path, exp := seed(t)
	storetest.AddDHCPServers(t, st)
	storetest.AddDHCPLeases(t, st)
	r := reader(t, st)
	ctx := context.Background()
	hosts, err := r.Hosts(ctx, store.HostFilter{Query: exp.Hosts["A"]})
	if err != nil || len(hosts) != 1 {
		t.Fatalf("host A: %v %+v", err, hosts)
	}
	calls := readCalls(r, hosts[0].HostID)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, table := range []string{
		"network_contexts", "context_prefixes", "hosts", "addresses", "address_sources", "name_records",
		"endpoints", "endpoint_results", "events", "observations", "observation_rollups",
		"dhcp_servers", "dhcp_leases", "dhcp_exchanges", "scans", "runtime_state",
	} {
		t.Run(table, func(t *testing.T) {
			rows, err := db.QueryContext(ctx, `SELECT name, type FROM pragma_table_info(?)`, table)
			if err != nil {
				t.Fatal(err)
			}
			var cols, types []string
			for rows.Next() {
				var c, typ string
				if err := rows.Scan(&c, &typ); err != nil {
					t.Fatal(err)
				}
				cols, types = append(cols, c), append(types, typ)
			}
			_ = rows.Close()
			var n int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || len(cols) == 0 {
				t.Fatalf("%s: %v %v", table, err, cols)
			}
			exec(t, st, `ALTER TABLE `+table+` RENAME TO damaged`)
			defer exec(t, st, `ALTER TABLE damaged RENAME TO `+table)
			failed := 0
			for i, col := range cols {
				// Text where a number is read, NULL where text is read.
				foreign := `NULL`
				if types[i] == "INTEGER" {
					foreign = `'x'`
				}
				sel := slices.Clone(cols)
				sel[i] = foreign + ` AS ` + col
				exec(t, st, `CREATE VIEW `+table+` AS SELECT `+strings.Join(sel, ", ")+` FROM damaged`)
				for name, call := range calls {
					if err := call(ctx); err != nil {
						failed++
						if !strings.Contains(err.Error(), path) {
							t.Errorf("%s.%s: %s: %v", table, col, name, err)
						}
					}
				}
				exec(t, st, `DROP VIEW `+table)
			}
			// A table without rows has no value to be foreign.
			if failed == 0 && n > 0 {
				t.Errorf("no read notices a foreign value in %s", table)
			}
		})
	}
}
