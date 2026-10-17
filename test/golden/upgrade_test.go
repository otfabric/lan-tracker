package golden

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/otfabric/lan-tracker/internal/clock"
	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/correlate"
	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/store"
	"github.com/otfabric/lan-tracker/migrations"
)

// lanSentinelSchema is the last migration LAN Sentinel released (a437a22).
const lanSentinelSchema = "0009"

// legacyRows is what a LAN Sentinel database holds after a while on site:
// two hosts on eth1, one described, one flagged proxy ARP, with addresses,
// names (one replaced), host-level services, identifications, an attempt,
// events of types LAN Tracker no longer emits and a persisted kill switch.
var legacyRows = []string{
	`INSERT INTO network_contexts (id, interface, first_seen, last_seen) VALUES (1, 'eth1', 1000, 9000)`,
	`INSERT INTO hosts (host_id, context_id, mac, vendor, presence, preferred_name, manufacturer, device_type, description, first_seen, last_seen)
		VALUES ('h1', 1, '00:1b:1b:aa:bb:01', 'Siemens AG', 'ACTIVE', 'plc-a.local', 'Siemens AG', 'Programmable Logic Controller',
			'Inverter, north building', 1000, 9000),
		('h2', 1, '00:00:5e:00:01:01', NULL, 'STALE', NULL, NULL, NULL, NULL, 1000, 5000)`,
	`INSERT INTO addresses (id, context_id, host_id, ip, family, first_seen, last_seen, ended_at)
		VALUES (1, 1, 'h1', '192.168.110.50', 4, 1000, 3000, 4000), (2, 1, 'h1', '192.168.110.51', 4, 4000, 9000, NULL),
		(3, 1, 'h2', '192.168.110.1', 4, 1000, 5000, NULL)`,
	`INSERT INTO address_sources (address_id, source, first_seen, last_seen) VALUES (2, 'passive_dhcp_lease', 4000, 9000)`,
	`INSERT INTO names (id, host_id, name, name_type, source, first_seen, last_seen, ended_at)
		VALUES (1, 'h1', 'plc-a', 'dhcp', 'passive_dhcp', 1000, 2000, 3000), (2, 'h1', 'plc-a2', 'dhcp', 'passive_dhcp', 3000, 8000, NULL),
		(3, 'h1', 'plc-a.local', 'mdns', 'passive_mdns', 1000, 9000, NULL)`,
	`INSERT INTO services (id, host_id, proto, port, state, first_seen, last_seen, last_result_at, detail_json)
		VALUES (1, 'h1', 'tcp', 502, 'OPEN', 1000, 9000, 9000, '{}'), (2, 'h1', 'udp', 44818, 'OPEN', 1000, 9000, 9000, '{"product_name":"X"}')`,
	`INSERT INTO identifications (host_id, field, value, confidence, source, evidence_json, first_seen, last_seen, current)
		VALUES ('h1', 'device_type', 'Programmable Logic Controller', 0.9, 'enip', '{}', 1000, 9000, 1),
		('h2', 'proxy_arp', 'true', 0.8, 'passive_arp', '{"reason":"answered ARP for 3 addresses"}', 5000, 5000, 1)`,
	`INSERT INTO identify_attempts (host_id, probe, result, attempted_at, trigger) VALUES ('h1', 'modbus', 'ok', 6000, 'scheduled')`,
	`INSERT INTO events (id, ts, type, severity, context_id, host_id, old_value, new_value, cause, evidence_json, clock_sync)
		VALUES (1, 1000, 'HOST_DISCOVERED', 'notice', 1, 'h1', NULL, '00:1b:1b:aa:bb:01', 'passive_arp', '{"ip":"192.168.110.50"}', 'synced'),
		(2, 4000, 'IP_CHANGED', 'notice', 1, 'h1', '192.168.110.50', '192.168.110.51', 'passive_dhcp_lease', '{}', 'synced'),
		(3, 5000, 'VENDOR_IDENTIFIED', 'info', 1, 'h2', NULL, 'proxy_arp=true', 'passive_arp', '{}', 'unknown'),
		(4, 6000, 'SERVICE_CLOSED', 'notice', 1, 'h1', 'tcp/502', 'TIMEOUT', 'tcp_connect', '{}', 'synced'),
		(5, 7000, 'IDENTIFY_RAN', 'info', 1, 'h1', NULL, 'modbus ok', 'operator', '{}', 'synced'),
		(6, 8000, 'HOST_DESCRIBED', 'notice', 1, 'h1', NULL, 'Inverter, north building', 'operator', '{"actor":"uid 0"}', 'synced')`,
	`INSERT INTO runtime_state (key, value, updated_at) VALUES ('active_disabled', '{"disabled":true,"reason":"commissioning","by":"uid 0"}', 8500)`,
	// The server's lease ACKs as LAN Sentinel's decoder kept them: the
	// client's MAC and yiaddr, the lease time and the server identifier.
	`INSERT INTO observations (ts, context_id, source, mac, ip, meta_json, host_id) VALUES
		(1500, 1, 'passive_dhcp_lease', '00:1b:1b:aa:bb:01', '192.168.110.50', '{"dhcp":"ack","lease_seconds":"3600","server_id":"192.168.110.1"}', 'h1'),
		(2500, 1, 'passive_dhcp_lease', '00:1b:1b:aa:bb:01', '192.168.110.50', '{"dhcp":"ack","lease_seconds":"3600","server_id":"192.168.110.1"}', 'h1'),
		(4000, 1, 'passive_dhcp_lease', '00:1b:1b:aa:bb:01', '192.168.110.51', '{"dhcp":"ack","lease_seconds":"infinite","server_id":"192.168.110.1"}', 'h1'),
		(4000, 1, 'passive_dhcp_server', '00:00:5e:00:01:01', '192.168.110.1', '{"dhcp":"ack","server_id":"192.168.110.1"}', 'h2')`,
}

// TestLANSentinelUpgrade upgrades a LAN Sentinel database (schema 0009)
// to the current schema: IDs, descriptions, bindings, events with their
// original types and wording, legacy identification rows and the kill
// switch survive; proxy ARP becomes the host flag; names become legacy
// records with their TTL unknown; the address-less services stay legacy
// rows and no endpoint address is invented; no new evidence kind is
// claimed. The correlator then resumes on it without re-announcing what is
// known (docs/MIGRATION.md).
func TestLANSentinelUpgrade(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hosts.db")
	all, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	old := fstest.MapFS{}
	for _, n := range all {
		if n[:4] <= lanSentinelSchema {
			b, _ := fs.ReadFile(migrations.FS, n)
			old[n] = &fstest.MapFile{Data: b}
		}
	}
	st, err := store.Open(ctx, store.Options{Path: path, Migrations: old})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Submit(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, q := range legacyRows {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(ctx); err != nil || st.OpErrors() != 0 {
		t.Fatalf("legacy rows: %v, %d op errors", err, st.OpErrors())
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	sim := clock.NewSim(time.UnixMilli(10000).UTC())
	st, err = store.Open(ctx, store.Options{Path: path, Clock: sim})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	query := func(q string) []string {
		t.Helper()
		var out []string
		if err := st.View(ctx, func(ctx context.Context, tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, q)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var s string
				if err := rows.Scan(&s); err != nil {
					return err
				}
				out = append(out, s)
			}
			return rows.Err()
		}); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}
	for q, want := range map[string][]string{
		`SELECT count(*) FROM pragma_foreign_key_check`:      {"0"},
		`SELECT integrity_check FROM pragma_integrity_check`: {"ok"},
		`SELECT host_id || ' ' || coalesce(description, '-') || ' ' || proxy_arp || ' ' || coalesce(last_frame_seen, 'unknown') FROM hosts ORDER BY host_id`: {
			"h1 Inverter, north building 0 unknown", "h2 - 1 unknown"},
		`SELECT id || ' ' || ip || ' ' || coalesce(ended_at, 'open') FROM addresses ORDER BY id`: {
			"1 192.168.110.50 4000", "2 192.168.110.51 open", "3 192.168.110.1 open"},
		`SELECT id || ' ' || type || ' ' || coalesce(old_value, '') || '>' || coalesce(new_value, '') FROM events ORDER BY id`: {
			"1 HOST_DISCOVERED >00:1b:1b:aa:bb:01", "2 IP_CHANGED 192.168.110.50>192.168.110.51", "3 VENDOR_IDENTIFIED >proxy_arp=true",
			"4 SERVICE_CLOSED tcp/502>TIMEOUT", "5 IDENTIFY_RAN >modbus ok", "6 HOST_DESCRIBED >Inverter, north building"},
		`SELECT name || ' ' || name_type || ' ' || record_type || ' ' || link || ' ' || coalesce(ttl_seconds, 'ttl unknown') || ' ' ||
			coalesce(ended_at, 'open') || ' ' || legacy FROM name_records ORDER BY id`: {
			"plc-a dhcp NAME legacy ttl unknown 3000 1", "plc-a2 dhcp NAME legacy ttl unknown open 1", "plc-a.local mdns NAME legacy ttl unknown open 1"},
		// The lease ACKs become a backfilled lease history: the latest open,
		// the earlier superseded; sender, relay and configuration unknown.
		`SELECT chaddr || ' ' || ip || ' ' || server_id || ' ' || first_ack || '..' || last_ack || ' ' || acks || ' ' ||
			coalesce(lease_seconds, '?') || ' ' || coalesce(expires_at, '-') || ' ' || coalesce(end_reason || '@' || ended_at, 'open') || ' ' ||
			backfilled || partial || ' ' || sender_mac || '|' || config_json || ' ' || coalesce(host_id, '-') FROM dhcp_leases ORDER BY id`: {
			"00:1b:1b:aa:bb:01 192.168.110.50 192.168.110.1 1500..2500 2 3600 3602500 superseded@4000 11 |{} h1",
			"00:1b:1b:aa:bb:01 192.168.110.51 192.168.110.1 4000..4000 1 -1 - open 11 |{} h1"},
		`SELECT count(*) FROM dhcp_exchanges`:                                                     {"0"},
		`SELECT count(*) FROM endpoints`:                                                          {"0"},
		`SELECT count(*) || ' ' || sum(port) FROM services`:                                       {"2 45320"},
		`SELECT count(*) FROM identifications`:                                                    {"2"},
		`SELECT count(*) FROM identify_attempts`:                                                  {"1"},
		`SELECT json_extract(value, '$.reason') FROM runtime_state WHERE key = 'active_disabled'`: {"commissioning"},
	} {
		if got := query(q); !slices.Equal(got, want) {
			t.Errorf("%s\n got %q\nwant %q", strings.Join(strings.Fields(q), " "), got, want)
		}
	}
	r, err := st.Reader(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if st, err := r.ActiveState(ctx); err != nil || !st.Disabled || st.Reason != "commissioning" {
		t.Errorf("kill switch after upgrade = %+v, %v", st, err)
	}
	h, err := r.Host(ctx, "h1")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Endpoints) != 0 || len(h.LegacyServices) != 2 || len(h.LegacyIdentifications) != 1 || h.LastFrameSeen != nil ||
		len(h.Names) != 3 || !h.Names[0].Legacy || h.Names[0].TTL != nil {
		t.Errorf("h1 = %+v", h)
	}

	// The correlator resumes: the known name is no news, a new one is.
	cfg := config.Defaults()
	cfg.Interfaces = []config.InterfaceConfig{{Name: "eth1"}}
	log := slog.New(slog.DiscardHandler)
	c, err := correlate.New(ctx, correlate.Options{Store: st, Clock: sim, Logger: log, Config: cfg, DataDriven: true,
		Events: events.NewEngine(st, log, func() platform.ClockState { return platform.ClockSynced })})
	if err != nil {
		t.Fatal(err)
	}
	mac, _ := net.ParseMAC("00:1b:1b:aa:bb:01")
	for i, o := range []observation.Observation{
		{Source: observation.PassiveDHCP, MAC: mac, Hostname: "plc-a2", NameType: observation.NameDHCP},
		{Source: observation.PassiveMDNS, MAC: mac, IP: netip.MustParseAddr("192.168.110.51"), Records: []observation.Record{
			{Name: "plc-a.local", Type: observation.RecordA, Addr: netip.MustParseAddr("192.168.110.51"), TTL: observation.TTLOf(120)},
			{Name: "plc-a-web.local", Type: observation.RecordA, Addr: netip.MustParseAddr("192.168.110.51"), TTL: observation.TTLOf(120)},
		}},
	} {
		o.Time, o.Interface = time.UnixMilli(int64(11000+i)).UTC(), "eth1"
		sim.Set(o.Time)
		c.Handle(ctx, observation.Message{Observation: o})
	}
	if err := st.Flush(ctx); err != nil || st.OpErrors() != 0 {
		t.Fatalf("resume: %v, %d op errors", err, st.OpErrors())
	}
	if got := query(`SELECT type || ' ' || coalesce(new_value, '') FROM events WHERE id > 6 ORDER BY id`); !slices.Equal(got,
		[]string{"HOSTNAME_ADDED mdns:plc-a-web.local"}) {
		t.Errorf("events after resuming = %q", got)
	}
	if got := query(`SELECT name || ' ' || legacy || ' ' || link FROM name_records WHERE name = 'plc-a2'`); !slices.Equal(got, []string{"plc-a2 0 sender"}) {
		t.Errorf("a confirmed legacy name = %q", got)
	}
}
