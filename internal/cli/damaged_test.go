package cli

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestReadsOfADamagedDatabase: when the database cannot answer (here its
// tables are gone, as another writer could leave them), every read
// command exits 2 with the cause on stderr and prints no partial table,
// through the daemon and with --offline alike (docs/CLI.md exit codes).
func TestReadsOfADamagedDatabase(t *testing.T) {
	f := serve(t)
	db, err := sql.Open("sqlite", "file:"+f.db)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range []string{"network_contexts", "hosts", "events", "observations", "observation_rollups", "endpoints",
		"dhcp_servers", "dhcp_leases", "dhcp_exchanges"} {
		if _, err := db.ExecContext(context.Background(), `ALTER TABLE `+table+` RENAME TO damaged_`+table); err != nil {
			t.Fatal(err)
		}
	}
	a := f.exp.Hosts["A"]
	for _, args := range [][]string{
		{"hosts", "list"}, {"hosts", "show", a}, {"hosts", "find", "192.168.110.50"}, {"hosts", "history", a},
		{"hosts", "evidence", a}, {"observations", "list"}, {"observations", "list", "--rollups"}, {"events", "list"},
		{"services", "list"}, {"services", "list", "--at", "2026-10-01T14:00:00Z"}, {"dhcp", "servers"}, {"dhcp", "leases"},
		{"dhcp", "exchanges"}, {"interfaces", "list"}, {"db", "info"},
	} {
		for _, mode := range []string{"online", "offline"} {
			run := args
			if mode == "offline" {
				run = append([]string{"--offline"}, args...)
			}
			code, out, stderr := f.run(t, run...)
			if code != ExitError || out != "" || !strings.Contains(stderr, "no such table") {
				t.Errorf("%s %v: exit %d\nstdout: %s\nstderr: %s", mode, args, code, out, stderr)
			}
		}
	}
}
