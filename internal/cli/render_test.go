package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/api"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/probe/scheduler"
	"github.com/otfabric/lan-tracker/internal/store"
)

// The host record, find and evidence tables print every fact the record
// carries, and say what a fact is not (docs/CLI.md §4, §5): an advertised
// name is not a host, a lease is the server's statement, a proxy ARP
// answer is not ownership.
func TestRecordRendering(t *testing.T) {
	now := time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 1, h, m, 0, 0, time.UTC) }
	ptr := func(t time.Time) *time.Time { return &t }
	i64 := func(v int64) *int64 { return &v }

	host := store.Host{
		HostSummary: store.HostSummary{
			HostID: "h1", Interface: "eth1", MAC: "02:00:00:00:00:01", LocallyAdministered: true, Presence: "STALE",
			PreferredName: "plc-a", Description: "Line 3 PLC", ProxyARP: true, FirstSeen: at(10, 0), LastSeen: at(14, 0),
			LastFrameSeen: ptr(at(14, 0)), LastAddressConfirmed: ptr(at(13, 0)), LastEndpointAnswer: ptr(at(14, 50)),
		},
		Addresses: []store.Binding{{IP: "192.168.110.50", FirstSeen: at(10, 0), LastSeen: at(12, 0), EndedAt: ptr(at(12, 0)),
			Sources: []store.SourceSeen{{Source: "passive_arp", FirstSeen: at(10, 0), LastSeen: at(12, 0)}}}},
		Names: []store.Name{
			{Name: "plc-a.local", Type: "mdns", RecordType: "A", Address: "192.168.110.50", Source: "passive_mdns", Link: "address",
				AdvertiserMAC: "02:00:00:00:00:09", TTL: i64(120), LastSeen: at(12, 0), EndedAt: ptr(at(12, 30)), EndReason: "goodbye", Freshness: "ended"},
			{Name: "_http._tcp", Type: "mdns", RecordType: "TXT", Value: "path=/", Source: "passive_mdns", Link: "sender",
				AdvertiserMAC: "02:00:00:00:00:01", LastSeen: at(14, 0), Freshness: "fresh"},
		},
		Endpoints: []store.Endpoint{
			{IP: "192.168.110.50", Proto: "tcp", Port: 502, LastResult: "TIMEOUT", LastResultAt: at(14, 50), LastDetail: "no answer in 2s",
				LastDefiniteResult: "OPEN", LastDefiniteAt: ptr(at(13, 0))},
			{IP: "192.168.110.50", Proto: "tcp", Port: 102, LastResult: "OPEN", LastResultAt: at(14, 0),
				LastDefiniteResult: "OPEN", LastDefiniteAt: ptr(at(14, 0))},
		},
		DHCPLeases: []store.DHCPLease{
			{IP: "192.168.110.51", ServerID: "192.168.110.1", FirstACK: at(11, 0), ACKs: 1, LeaseSeconds: i64(3600),
				EndedAt: ptr(at(12, 0)), EndReason: "expired", Config: map[string]any{"gateway": "192.168.110.1"}, Relay: "10.0.0.1"},
			{IP: "192.168.110.52", FirstACK: at(13, 30), ACKs: 2, ExpiresAt: ptr(at(17, 30))},
		},
	}

	tests := []struct {
		name   string
		render func(a *app, w *bytes.Buffer)
		want   []string
		absent []string
	}{
		{"host record", func(a *app, w *bytes.Buffer) { a.hostRecord(w, host, nil) }, []string{
			"MAC:         02:00:00:00:00:01  (locally administered)",
			"Name:        plc-a\n", "Description: Line 3 PLC\n",
			"Proxy ARP:   answers ARP for addresses other hosts hold; its ARP replies do not prove ownership",
			"(frame 1h ago, address confirmed 2h ago, endpoint answer 10m ago)",
			"192.168.110.50   2026-10-01 10:00:00 → 2026-10-01 12:00:00   sources: passive_arp",
			"plc-a.local", "ended 2026-10-01 12:30:00 (goodbye)", "path=/",
			"tcp/502   TIMEOUT", "(no answer in 2s)  last definite: OPEN 2h ago",
			"from 192.168.110.1   2026-10-01 11:00:00 → expired 2026-10-01 12:00:00  acks 1  lease 1h0m0s  gateway 192.168.110.1  via relay 10.0.0.1",
			"192.168.110.52   from -", "lease -  expires 2026-10-01 17:30:00",
		}, []string{"last definite: OPEN 50m ago", "advertised by", "ttl 120s"}},
		{"evidence names in detail", func(a *app, w *bytes.Buffer) {
			a.evidenceTable(w, []store.Evidence{{Host: host}, {Host: store.Host{HostSummary: store.HostSummary{HostID: "h2", MAC: "02:00:00:00:00:02", Interface: "eth1"}}}})
		}, []string{
			"Host h1  02:00:00:00:00:01  on eth1", "      passive_arp      first 2026-10-01 10:00:00",
			"passive_mdns advertised by 02:00:00:00:00:09 ttl 120s link address",
			"passive_mdns link sender", strings.Repeat("-", 72), "Host h2",
		}, []string{"advertised by 02:00:00:00:00:01"}},
		{"nobody holds it", func(a *app, w *bytes.Buffer) {
			a.findResult(w, store.FindResult{Query: "192.168.110.60", At: ptr(at(12, 0)),
				Previous: []store.Holding{{Interface: "eth1", MAC: "02:00:00:00:00:01", Binding: store.Binding{IP: "192.168.110.60", EndedAt: ptr(at(11, 0))}}},
				Next:     []store.Holding{{Interface: "eth1", MAC: "02:00:00:00:00:02", Binding: store.Binding{IP: "192.168.110.60", FirstSeen: at(13, 0)}}},
				Advertised: []store.Name{{Name: "plc-x.local", Type: "mdns", RecordType: "A", Address: "192.168.110.60", Source: "passive_mdns",
					AdvertiserMAC: "02:00:00:00:00:09", LastSeen: at(14, 0), Freshness: "fresh"},
					{Name: "plc-x.local", Type: "dns", RecordType: "TXT", Value: "v=1", Source: "dns_query", LastSeen: at(14, 0), Freshness: "stale"}},
			})
		}, []string{
			"No holder of 192.168.110.60 at 2026-10-01 12:00:00.",
			"Previous:    192.168.110.60 held by 02:00:00:00:00:01 on eth1 until 2026-10-01 11:00:00",
			"Next:        192.168.110.60 held by 02:00:00:00:00:02 on eth1 from 2026-10-01 13:00:00",
			"held by no single MAC (a name record, not a host)", "passive_mdns from 02:00:00:00:00:09", "v=1",
		}, nil},
		{"holder at a time", func(a *app, w *bytes.Buffer) {
			fh := store.FoundHost{Host: store.Host{HostSummary: store.HostSummary{HostID: "h1", Interface: "eth1", MAC: "02:00:00:00:00:01",
				Vendor: "Siemens AG", PreferredName: "plc-a", Description: "Line 3 PLC"}},
				Binding: &store.Binding{IP: "192.168.110.50", FirstSeen: at(10, 0), LastSeen: at(11, 0)}, Conflict: true,
				ConflictEvent: &store.Event{TS: at(10, 30), Type: "DUPLICATE_IP_DETECTED", New: "192.168.110.50", MAC: "02:00:00:00:00:01", RelatedMAC: "02:00:00:00:00:02"}}
			a.findResult(w, store.FindResult{Query: "192.168.110.50", At: ptr(at(11, 30)), Hosts: []store.FoundHost{fh, fh}})
		}, []string{
			"MAC:         02:00:00:00:00:01  (OUI registrant: Siemens AG)", "Name:        plc-a\n", "Description: Line 3 PLC\n",
			"CONFLICT   (unconfirmed since 2026-10-01 11:00:00)",
			"Conflict:    2026-10-01 10:30:00 DUPLICATE_IP_DETECTED 192.168.110.50 between 02:00:00:00:00:01 and 02:00:00:00:00:02",
			"\n\nHost ID:     h1",
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			a := &app{env: Env{Stdout: &buf, Location: time.UTC, Now: func() time.Time { return now }}, g: globals{output: "table"}}
			tt.render(a, &buf)
			out := buf.String()
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, w := range tt.absent {
				if strings.Contains(out, w) {
					t.Errorf("output has %q:\n%s", w, out)
				}
			}
		})
	}
}

// daemon status says when active discovery was forced off by the
// environment, when event times are marked, when a scan is still running,
// and how a pass went: cut short, or longer than its interval.
func TestStatusRendering(t *testing.T) {
	now := time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)
	pass := &scheduler.PassSummary{At: now.Add(-3 * time.Minute), Seconds: 1, Probed: 4, Replied: 2, Overran: true, Complete: true}
	for _, tt := range []struct {
		name string
		st   api.Status
		want []string
	}{
		{"forced off, unsynced", api.Status{Replay: true, Active: store.ActiveState{Disabled: true, Forced: true, Reason: "ignored"},
			Clock: platform.ClockUnsynced, LastScan: &store.Scan{Started: now, Interface: "eth1", Kind: "arp", Trigger: "operator"},
			Database: api.DatabaseStatus{OK: true, Size: 2e9},
			Interfaces: []api.InterfaceStatus{{InterfaceInfo: store.InterfaceInfo{Name: "eth1", State: "up"},
				Collectors: []api.CollectorStatus{{CollectorStatus: platform.CollectorStatus{Collector: platform.CollectorNames, State: platform.StateRunning, Backend: "socket"},
					LastPass: pass}}}},
			State: api.StateOK}, []string{
			"   replay\n", "2.0 GB", "Active:     DISABLED (LAN_TRACKER_ACTIVE_DISABLED is set)\n",
			"Clock:      unsynced (events are marked until the clock is synchronised)",
			"Last scan:  2026-10-01 15:00:00  eth1  arp (operator)  running",
			"last pass 3m ago: 4 asked, 2 replied (1 s, longer than its interval: the cadence is not kept)",
		}},
		{"forced off by the environment", api.Status{Active: store.ActiveState{Disabled: true, Forced: true, Reason: "LAN_TRACKER_ACTIVE_DISABLED=yes"},
			Database: api.DatabaseStatus{OK: true}, State: api.StateOK}, []string{
			"Active:     DISABLED (LAN_TRACKER_ACTIVE_DISABLED=yes)\n",
		}},
		{"no clock, no scan", api.Status{Database: api.DatabaseStatus{OK: true, Size: 1500}, State: api.StateOK}, []string{
			"Active:     enabled\n", "Clock:      unknown\n", "Last scan:  never\n", "2 KB",
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			a := &app{env: Env{Stdout: &buf, Location: time.UTC, Now: func() time.Time { return now }}}
			a.statusText(&buf, tt.st)
			for _, w := range tt.want {
				if !strings.Contains(buf.String(), w) {
					t.Errorf("status lacks %q:\n%s", w, buf.String())
				}
			}
		})
	}
}
