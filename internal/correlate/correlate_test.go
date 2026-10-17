package correlate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/clock"
	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/identify"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/store"
)

var t0 = time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)

type harness struct {
	t    *testing.T
	ctx  context.Context
	path string
	st   *store.Store
	c    *Correlator
	sim  *clock.Sim
	ids  int
}

func testConfig() *config.Config {
	cfg := config.Defaults()
	cfg.Interfaces = []config.InterfaceConfig{{Name: "eth0"}, {Name: "eth1"}}
	return cfg
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, ctx: context.Background(), path: filepath.Join(t.TempDir(), "hosts.db"), sim: clock.NewSim(t0)}
	h.open()
	h.link("eth0", true, "10.0.0.0/24", "fd00::/64")
	h.link("eth1", true, "10.1.0.0/24")
	return h
}

func (h *harness) open() {
	h.t.Helper()
	st, err := store.Open(h.ctx, store.Options{Path: h.path, Clock: h.sim})
	if err != nil {
		h.t.Fatal(err)
	}
	vendors, err := identify.Embedded()
	if err != nil {
		h.t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	c, err := New(h.ctx, Options{
		Store: st, Events: events.NewEngine(st, log, nil), Vendors: vendors, Clock: h.sim, Logger: log,
		Config: testConfig(), DataDriven: true,
		NewID: func() string { h.ids++; return fmt.Sprintf("host-%02d", h.ids) },
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.st, h.c = st, c
	h.t.Cleanup(func() { _ = st.Close() })
}

// restart closes the store and builds a new correlator from the database.
func (h *harness) restart() {
	h.t.Helper()
	if err := h.st.Close(); err != nil {
		h.t.Fatal(err)
	}
	h.open()
}

func (h *harness) link(iface string, up bool, prefixes ...string) {
	ls := observation.LinkState{Time: h.sim.Now(), Interface: iface, Present: true, Up: up}
	for _, p := range prefixes {
		ls.Prefixes = append(ls.Prefixes, netip.MustParsePrefix(p))
	}
	h.c.Handle(h.ctx, observation.Message{Link: &ls})
}

func (h *harness) at(d time.Duration) time.Time { return t0.Add(d) }

// obs feeds an observation at t0+d. mac or ip may be empty.
func (h *harness) obs(d time.Duration, src observation.Source, iface, mac, ip string) {
	h.t.Helper()
	o := observation.Observation{Time: h.at(d), Source: src, Interface: iface}
	if mac != "" {
		m, err := net.ParseMAC(mac)
		if err != nil {
			h.t.Fatal(err)
		}
		o.MAC = m
	}
	if ip != "" {
		o.IP = netip.MustParseAddr(ip)
	}
	h.sim.Set(o.Time)
	h.c.Handle(h.ctx, observation.Message{Observation: o})
}

func (h *harness) flush() {
	h.t.Helper()
	if err := h.st.Flush(h.ctx); err != nil {
		h.t.Fatal(err)
	}
	if h.st.OpErrors() != 0 {
		h.t.Fatalf("%d store writes failed", h.st.OpErrors())
	}
}

func (h *harness) query(q string, scan func(*sql.Rows) string) []string {
	h.t.Helper()
	h.flush()
	var out []string
	err := h.st.View(h.ctx, func(ctx context.Context, tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			out = append(out, scan(rows))
		}
		return rows.Err()
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

// events lists "TYPE host old>new" for every event, with offsets from t0.
func (h *harness) events() []string {
	return h.query(`SELECT ts, type, coalesce(host_id, '-'), coalesce(old_value, ''), coalesce(new_value, '') FROM events ORDER BY id`,
		func(r *sql.Rows) string {
			var ts int64
			var typ, host, old, nw string
			if err := r.Scan(&ts, &typ, &host, &old, &nw); err != nil {
				h.t.Fatal(err)
			}
			return fmt.Sprintf("%s %s %s %s>%s", time.UnixMilli(ts).UTC().Sub(t0), typ, host, old, nw)
		})
}

// bindings lists "host ip [first,ended) conflict" for every address row.
func (h *harness) bindings() []string {
	return h.query(`SELECT host_id, ip, first_seen, coalesce(ended_at, -1), conflict FROM addresses ORDER BY id`,
		func(r *sql.Rows) string {
			var host, ip string
			var first, ended int64
			var conflict int
			if err := r.Scan(&host, &ip, &first, &ended, &conflict); err != nil {
				h.t.Fatal(err)
			}
			end := "open"
			if ended >= 0 {
				end = time.UnixMilli(ended).UTC().Sub(t0).String()
			}
			return fmt.Sprintf("%s %s [%s,%s) c=%d", host, ip, time.UnixMilli(first).UTC().Sub(t0), end, conflict)
		})
}

func expect(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\ngot:\n  %s\nwant:\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// hostEvents drops context events (SUBNET_CHANGED, INTERFACE_*).
func hostEvents(all []string) []string {
	var out []string
	for _, e := range all {
		if !strings.Contains(e, " - ") {
			out = append(out, e)
		}
	}
	return out
}

func TestSourceCounts(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.obs(time.Second, observation.TCPConnect, "eth0", "", "10.0.0.99")
	processed, unbound := h.c.SourceCounts()
	if processed[observation.PassiveARP] != 1 || processed[observation.TCPConnect] != 1 || unbound[observation.TCPConnect] != 1 ||
		unbound[observation.PassiveARP] != 0 || len(processed) != len(observation.Sources) {
		t.Errorf("processed %v unbound %v", processed, unbound)
	}
}

const (
	macA = "00:1b:1b:00:00:0a"
	macB = "00:1b:1b:00:00:0b"
	macC = "00:1b:1b:00:00:0c"
)

// The same MAC on another interface is another host, and only "also
// observed" there: seeing it twice does not establish that it moved.
func TestMACOnTwoInterfacesIsAnotherHost(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.obs(time.Minute, observation.PassiveARP, "eth1", macA, "10.1.0.5")
	expect(t, "events", hostEvents(h.events()), []string{
		"0s HOST_DISCOVERED host-01 >00:1b:1b:00:00:0a",
		"1m0s HOST_DISCOVERED host-02 >00:1b:1b:00:00:0a",
		"1m0s MAC_ALSO_OBSERVED host-02 eth0>eth1",
	})
}

func TestIPv6AddressesAccumulate(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveNDP, "eth0", macA, "fe80::1")
	h.obs(time.Hour, observation.PassiveNDP, "eth0", macA, "fd00::1")
	h.obs(2*time.Hour, observation.PassiveNDP, "eth0", macA, "fd00::2")
	expect(t, "bindings", h.bindings(), []string{
		"host-01 fe80::1 [0s,open) c=0",
		"host-01 fd00::1 [1h0m0s,open) c=0",
		"host-01 fd00::2 [2h0m0s,open) c=0",
	})
}

// A production and a maintenance address on one MAC stay independent,
// however far apart they were first seen (docs/DATA_MODEL.md §5.3).
func TestIPv4MultipleAddressesAreIndependent(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.obs(time.Minute, observation.PassiveARP, "eth0", macA, "10.0.0.6")
	h.obs(3*time.Hour, observation.PassiveDHCP, "eth0", macA, "10.0.0.7") // a new address closes no other
	expect(t, "bindings", h.bindings(), []string{
		"host-01 10.0.0.5 [0s,open) c=0",
		"host-01 10.0.0.6 [1m0s,open) c=0",
		"host-01 10.0.0.7 [3h0m0s,open) c=0",
	})
	expect(t, "events", hostEvents(h.events()), []string{
		"0s HOST_DISCOVERED host-01 >00:1b:1b:00:00:0a",
		"1m0s IP_ADDED host-01 >10.0.0.6",
		"3h0m0s IP_ADDED host-01 >10.0.0.7",
	})
}

func TestOnLinkCheck(t *testing.T) {
	h := newHarness(t)
	// Routed traffic: the frame's source MAC is the router's.
	h.obs(0, observation.PassiveIPv4, "eth0", macA, "8.8.8.8")
	// In-subnet source address is attributed.
	h.obs(time.Second, observation.PassiveIPv4, "eth0", macA, "10.0.0.1")
	// ARP is L2 evidence even outside the subnet.
	h.obs(2*time.Second, observation.PassiveARP, "eth0", macB, "192.168.99.7")
	// Network and broadcast addresses are not host addresses.
	h.obs(3*time.Second, observation.PassiveARP, "eth0", macC, "10.0.0.255")
	expect(t, "bindings", h.bindings(), []string{
		"host-01 10.0.0.1 [1s,open) c=0",
		"host-02 192.168.99.7 [2s,open) c=0",
	})
}

func TestTakeoverFromHostThatIsNotLive(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.obs(time.Hour, observation.PassiveARP, "eth0", macB, "10.0.0.5") // A is STALE: takeover
	expect(t, "events", hostEvents(h.events()), []string{
		"0s HOST_DISCOVERED host-01 >00:1b:1b:00:00:0a",
		"1h0m0s HOST_DISCOVERED host-02 >00:1b:1b:00:00:0b",
		"1h0m0s IP_REMOVED host-01 10.0.0.5>",
	})
	expect(t, "bindings", h.bindings(), []string{
		"host-01 10.0.0.5 [0s,1h0m0s) c=0",
		"host-02 10.0.0.5 [1h0m0s,open) c=0",
	})
}

func TestUnboundWhenHolderIsAmbiguous(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.obs(time.Second, observation.PassiveARP, "eth0", macB, "10.0.0.5") // conflict: two holders
	h.obs(2*time.Second, observation.TCPConnect, "eth0", "", "10.0.0.5")
	h.obs(3*time.Second, observation.TCPConnect, "eth0", "", "10.0.0.9")
	if got := h.c.Unbound(); got != 2 {
		t.Errorf("unbound = %d, want 2", got)
	}
	got := h.query(`SELECT count(*) FROM observations WHERE host_id IS NULL`, func(r *sql.Rows) string {
		var n int
		_ = r.Scan(&n)
		return fmt.Sprint(n)
	})
	expect(t, "unbound rows", got, []string{"2"})
}

func TestIgnoredObservations(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth9", macA, "10.0.0.5")                // unconfigured interface
	h.obs(0, observation.PassiveMDNS, "eth0", "01:00:5e:00:00:fb", "")        // multicast MAC only
	h.obs(0, observation.PassiveDHCP, "eth0", "ff:ff:ff:ff:ff:ff", "0.0.0.0") // broadcast/unspecified
	if got := h.c.Ignored(); got != 3 {
		t.Errorf("ignored = %d, want 3", got)
	}
	expect(t, "events", hostEvents(h.events()), nil)
}

func TestExpiryAndPresence(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.obs(time.Minute, observation.PassiveARP, "eth0", macA, "10.0.0.6")
	// .6 keeps being confirmed; .5 is not, and expires 24h after its last
	// confirmation. Then the host goes quiet and becomes MISSING 24h after
	// its last observation, and reappears later.
	h.obs(20*time.Hour, observation.PassiveARP, "eth0", macA, "10.0.0.6")
	h.obs(25*time.Hour, observation.PassiveARP, "eth0", macA, "10.0.0.6")
	h.obs(60*time.Hour, observation.PassiveARP, "eth0", macB, "10.0.0.20") // advances time
	h.obs(61*time.Hour, observation.PassiveARP, "eth0", macA, "10.0.0.6")
	expect(t, "events", hostEvents(h.events()), []string{
		"0s HOST_DISCOVERED host-01 >00:1b:1b:00:00:0a",
		"1m0s IP_ADDED host-01 >10.0.0.6",
		"24h0m0s IP_REMOVED host-01 10.0.0.5>",
		"49h0m0s HOST_DISAPPEARED host-01 STALE>MISSING",
		"60h0m0s HOST_DISCOVERED host-02 >00:1b:1b:00:00:0b",
		"61h0m0s HOST_REAPPEARED host-01 MISSING>ACTIVE",
	})
}

func TestLateObservationDoesNotReopen(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.obs(time.Hour, observation.PassiveDHCP, "eth0", macA, "10.0.0.6")
	// A neighbour-table entry for an address the host has never had,
	// confirmed before the host's last change: it opens nothing.
	h.obs(30*time.Minute, observation.KernelNeighbor, "eth0", macA, "10.0.0.9")
	// One for an address it holds refreshes it.
	h.obs(40*time.Minute, observation.KernelNeighbor, "eth0", macA, "10.0.0.5")
	expect(t, "bindings", h.bindings(), []string{
		"host-01 10.0.0.5 [0s,open) c=0",
		"host-01 10.0.0.6 [1h0m0s,open) c=0",
	})
}

func TestServices(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	probe := func(d time.Duration, state observation.ServiceState) {
		o := observation.Observation{Time: h.at(d), Source: observation.TCPConnect, Interface: "eth0",
			IP: netip.MustParseAddr("10.0.0.5"), Service: &observation.ServiceResult{Proto: "tcp", Port: 502, State: state}}
		h.c.Handle(h.ctx, observation.Message{Observation: o})
	}
	probe(time.Minute, observation.ServiceRefused)
	probe(2*time.Minute, observation.ServiceOpen)
	probe(3*time.Minute, observation.ServiceOpen) // no event
	probe(4*time.Minute, observation.ServiceTimeout)
	// Events mark the endpoint opening and leaving OPEN, with the exact
	// results; repeats and the first REFUSED are no event.
	expect(t, "events", hostEvents(h.events()), []string{
		"0s HOST_DISCOVERED host-01 >00:1b:1b:00:00:0a",
		"2m0s SERVICE_STATE_CHANGED host-01 tcp/502 REFUSED>tcp/502 OPEN",
		"4m0s SERVICE_STATE_CHANGED host-01 tcp/502 OPEN>tcp/502 TIMEOUT",
	})
	// The timeout did not erase that the endpoint last answered OPEN.
	expect(t, "endpoint", h.query(`SELECT ip || ' ' || port || ' ' || last_result || ' ' || last_definite_result || ' ' ||
		(last_definite_at - `+fmt.Sprint(t0.UnixMilli())+`) || ' ' || host_id FROM endpoints`, scanText), []string{"10.0.0.5 502 TIMEOUT OPEN 180000 host-01"})
	expect(t, "history", h.query(`SELECT result FROM endpoint_results ORDER BY id`, scanText), []string{"REFUSED", "OPEN", "TIMEOUT"})
}

func TestLinkEvents(t *testing.T) {
	h := newHarness(t)
	h.sim.Set(h.at(time.Hour))
	h.link("eth0", false, "10.0.0.0/24", "fd00::/64")
	h.sim.Set(h.at(2 * time.Hour))
	h.link("eth0", true, "10.0.2.0/24")
	var ctxEvents []string
	for _, e := range h.events() {
		if strings.Contains(e, " - ") {
			ctxEvents = append(ctxEvents, e)
		}
	}
	expect(t, "context events", ctxEvents, []string{
		"0s SUBNET_CHANGED - >10.0.0.0/24",
		"0s SUBNET_CHANGED - >fd00::/64",
		"0s SUBNET_CHANGED - >10.1.0.0/24",
		"1h0m0s INTERFACE_DOWN - >down",
		"2h0m0s INTERFACE_UP - >up",
		"2h0m0s SUBNET_CHANGED - 10.0.0.0/24>",
		"2h0m0s SUBNET_CHANGED - fd00::/64>",
		"2h0m0s SUBNET_CHANGED - >10.0.2.0/24",
	})
}

func TestStateSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.obs(time.Second, observation.PassiveARP, "eth0", macB, "10.0.0.5") // conflict
	h.restart()
	h.link("eth0", true, "10.0.0.0/24", "fd00::/64") // unchanged: no SUBNET_CHANGED
	h.link("eth1", true, "10.1.0.0/24")
	// Known host, known binding: no new events. A gains another address;
	// the conflict on .5 stays until one side's binding ends.
	h.obs(time.Minute, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.obs(time.Hour, observation.PassiveDHCP, "eth0", macA, "10.0.0.7")
	expect(t, "events", hostEvents(h.events()), []string{
		"0s HOST_DISCOVERED host-01 >00:1b:1b:00:00:0a",
		"1s HOST_DISCOVERED host-02 >00:1b:1b:00:00:0b",
		"1s DUPLICATE_IP_DETECTED host-02 >10.0.0.5",
		"1h0m0s IP_ADDED host-01 >10.0.0.7",
	})
	if n := len(h.events()) - len(hostEvents(h.events())); n != 3 {
		t.Errorf("%d context events, want only the 3 from the first start", n)
	}
}

func TestOUIRegistrant(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", "00:1b:1b:12:34:56", "10.0.0.5")
	h.obs(0, observation.PassiveARP, "eth0", "02:42:ac:11:00:02", "10.0.0.6")
	got := h.query(`SELECT mac, coalesce(oui_registrant, '-'), locally_administered FROM hosts ORDER BY mac`,
		func(r *sql.Rows) string {
			var mac, registrant string
			var la int
			_ = r.Scan(&mac, &registrant, &la)
			return fmt.Sprintf("%s %s la=%d", mac, registrant, la)
		})
	// The OUI registrant is a hint on the MAC prefix, never the maker
	// (ADR 0011); a locally administered MAC has none.
	expect(t, "hosts", got, []string{
		"00:1b:1b:12:34:56 Siemens AG la=0",
		"02:42:ac:11:00:02 - la=1",
	})
}

// The same address on two interfaces is two contexts: two hosts with one
// binding each, no conflict, and one endpoint per interface.
func TestSameIPOnTwoInterfaces(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "192.168.1.10")
	h.obs(time.Second, observation.PassiveARP, "eth1", macB, "192.168.1.10")
	for _, iface := range []string{"eth0", "eth1"} {
		o := tcpResult("192.168.1.10", 502, observation.ServiceOpen)
		o.Time, o.Interface = h.at(time.Minute), iface
		h.c.Handle(h.ctx, observation.Message{Observation: o})
	}
	expect(t, "bindings", h.bindings(), []string{
		"host-01 192.168.1.10 [0s,open) c=0",
		"host-02 192.168.1.10 [1s,open) c=0",
	})
	if got := h.query(`SELECT count(*) FROM events WHERE type = 'DUPLICATE_IP_DETECTED'`, scanText); got[0] != "0" {
		t.Errorf("a conflict across interfaces: %v", got)
	}
	expect(t, "endpoints", h.query(`SELECT context_id || ' ' || host_id FROM endpoints ORDER BY context_id`, scanText),
		[]string{"1 host-01", "2 host-02"})
}
