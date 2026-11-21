package correlate

import (
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/identify"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/store"
)

// TestLoadRefusesDamagedState starts the correlator on a database another
// writer damaged: a table gone, or a row it cannot read back (a MAC, an
// address or a prefix that does not parse, a host or binding whose parent
// is missing). The correlator must refuse to start rather than run on a
// partial state and assign row ids that already exist.
func TestLoadRefusesDamagedState(t *testing.T) {
	tests := []struct {
		name   string
		damage string
		want   string
	}{
		{"no contexts", `ALTER TABLE network_contexts RENAME TO damaged`, "no such table: network_contexts"},
		{"no prefixes", `ALTER TABLE context_prefixes RENAME TO damaged`, "no such table: context_prefixes"},
		{"no hosts", `ALTER TABLE hosts RENAME TO damaged`, "no such table: hosts"},
		{"no addresses", `ALTER TABLE addresses RENAME TO damaged`, "no such table: addresses"},
		{"no scans", `ALTER TABLE scans RENAME TO damaged`, "no such table: scans"},
		{"no dhcp servers", `ALTER TABLE dhcp_servers RENAME TO damaged`, "no such table: dhcp_servers"},
		{"no leases", `ALTER TABLE dhcp_leases RENAME TO damaged`, "no such table: dhcp_leases"},
		{"no endpoints", `ALTER TABLE endpoints RENAME TO damaged`, "no such table: endpoints"},
		{"no name records", `ALTER TABLE name_records RENAME TO damaged`, "no such table: name_records"},
		{"no observations", `ALTER TABLE observations RENAME TO damaged`, "no such table: observations"},
		{"no endpoint results", `ALTER TABLE endpoint_results RENAME TO damaged`, "no such table: endpoint_results"},
		{"bad mac", `UPDATE hosts SET mac = 'not-a-mac' WHERE host_id = 'host-01'`, "not-a-mac"},
		{"bad address", `UPDATE addresses SET ip = '10.0.0.999' WHERE ended_at IS NULL`, "10.0.0.999"},
		{"bad prefix", `UPDATE context_prefixes SET prefix = '10.0.0.0/99' WHERE prefix = '10.0.0.0/24'`, "10.0.0.0/99"},
		{"orphan host", `UPDATE hosts SET context_id = 99 WHERE host_id = 'host-01'`, "references unknown context 99"},
		{"orphan address", `UPDATE addresses SET host_id = 'gone' WHERE ended_at IS NULL`, "references unknown host gone"},
		// STRICT tables refuse a value of the wrong type; a view in place
		// of the table stands for one written without them.
		{"foreign context row", `ALTER TABLE network_contexts RENAME TO damaged;
			CREATE VIEW network_contexts AS SELECT id, interface, 'x' AS last_seen FROM damaged`, "last_seen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
			h.obs(time.Second, observation.PassiveARP, "eth0", macB, "10.0.0.6")
			h.flush()
			if err := h.st.Close(); err != nil {
				t.Fatal(err)
			}

			db, err := sql.Open("sqlite", "file:"+h.path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(h.ctx, tt.damage); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()

			st, err := store.Open(h.ctx, store.Options{Path: h.path, Clock: h.sim})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			rd, err := st.Reader(time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			defer rd.Close()
			vendors, err := identify.Embedded()
			if err != nil {
				t.Fatal(err)
			}
			log := slog.New(slog.DiscardHandler)
			_, err = New(h.ctx, Options{Store: st, Reader: rd, Events: events.NewEngine(st, log, nil), Vendors: vendors, Clock: h.sim, Logger: log,
				Config: testConfig(), DataDriven: true})
			if err == nil || !strings.Contains(err.Error(), "load state") || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("start on a damaged database: %v", err)
			}
		})
	}
}
