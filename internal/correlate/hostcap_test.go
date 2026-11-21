package correlate

import (
	"database/sql"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/observation"
)

const (
	macD = "00:1b:1b:00:00:0d"
	macE = "00:1b:1b:00:00:0e"
	macF = "00:1b:1b:00:00:0f"
)

// setCap reloads the configuration with identity.max_hosts_per_interface
// set to n.
func (h *harness) setCap(n int) {
	cfg := testConfig()
	cfg.Identity.MaxHostsPerInterface = n
	h.c.SetConfig(cfg)
}

// capEvents lists the events of the host cap and those around it, "TYPE
// host old>new cause" in order.
func (h *harness) capEvents() []string {
	return h.query(`SELECT type, coalesce(host_id, '-'), coalesce(old_value, ''), coalesce(new_value, ''), cause FROM events
		WHERE type IN ('HOST_EVICTED', 'HOST_CAP_REACHED', 'HOST_DISCOVERED', 'HOST_REAPPEARED', 'IP_REMOVED', 'IP_ADDED',
		'HOSTNAME_REMOVED', 'HOST_DESCRIBED') ORDER BY id`,
		func(r *sql.Rows) string {
			var typ, host, old, nw, cause string
			if err := r.Scan(&typ, &host, &old, &nw, &cause); err != nil {
				h.t.Fatal(err)
			}
			return fmt.Sprintf("%s %s %s>%s %s", typ, host, old, nw, cause)
		})
}

func (h *harness) evictedHosts() []string {
	return h.query(`SELECT host_id, mac FROM hosts WHERE evicted_at IS NOT NULL ORDER BY host_id`, func(r *sql.Rows) string {
		var id, mac string
		if err := r.Scan(&id, &mac); err != nil {
			h.t.Fatal(err)
		}
		return id + " " + mac
	})
}

// A new MAC on a full interface evicts the host missing the longest; with
// none missing its observations are dropped, HOST_CAP_REACHED once. The
// evicted MAC seen again is the same host (AGENTS.md rule 6), also after a
// restart.
func TestHostCap(t *testing.T) {
	h := newHarness(t)
	h.setCap(2)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.named(time.Minute, observation.PassiveMDNS, macA, "", observation.NameMDNS, "plc-a")
	h.obs(time.Hour, observation.PassiveARP, "eth0", macB, "10.0.0.6")
	// Both missing (stale 24h); A for longer.
	h.obs(26*time.Hour, observation.PassiveIPv4, "eth0", macC, "")
	h.obs(26*time.Hour+time.Second, observation.PassiveIPv4, "eth0", macD, "")
	// C and D are active: E and F are refused, one event.
	h.obs(26*time.Hour+2*time.Second, observation.PassiveIPv4, "eth0", macE, "")
	h.obs(26*time.Hour+3*time.Second, observation.PassiveIPv4, "eth0", macF, "")
	// Another interface has its own cap.
	h.obs(26*time.Hour+4*time.Second, observation.PassiveIPv4, "eth1", macE, "")

	expect(t, "events", h.capEvents(), []string{
		"HOST_DISCOVERED host-01 >00:1b:1b:00:00:0a passive_arp",
		"HOST_DISCOVERED host-02 >00:1b:1b:00:00:0b passive_arp",
		"IP_REMOVED host-01 10.0.0.5> expiry", // seen (mDNS) after its last ARP
		"HOST_EVICTED host-01 MISSING> host_cap",
		"HOST_DISCOVERED host-03 >00:1b:1b:00:00:0c passive_ipv4",
		"IP_REMOVED host-02 10.0.0.6> host_cap",
		"HOST_EVICTED host-02 MISSING> host_cap",
		"HOST_DISCOVERED host-04 >00:1b:1b:00:00:0d passive_ipv4",
		"HOST_CAP_REACHED - >2 passive_ipv4",
		"HOST_DISCOVERED host-05 >00:1b:1b:00:00:0e passive_ipv4",
	})
	expect(t, "evicted", h.evictedHosts(), []string{"host-01 00:1b:1b:00:00:0a", "host-02 00:1b:1b:00:00:0b"})
	if got := h.query(`SELECT end_reason FROM name_records`, scanText); strings.Join(got, ",") != endHostEvicted {
		t.Errorf("name records of the evicted host: %v", got)
	}
	if got := h.query(`SELECT count(*) FROM observations WHERE mac IN ('`+macF+`')`, scanText); got[0] != "0" {
		t.Errorf("refused observations stored: %v", got)
	}
	counts := h.c.CapCounts()
	if counts["eth0"] != (CapCounts{HostsEvicted: 2, ObservationsRefused: 2}) || counts["eth1"] != (CapCounts{}) {
		t.Errorf("counts %+v", counts)
	}

	// Room again: A comes back as host-01 and the refusal state clears.
	h.setCap(3)
	h.obs(27*time.Hour, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.setCap(3)
	h.obs(27*time.Hour+time.Second, observation.PassiveIPv4, "eth0", macF, "")
	got := h.capEvents()
	expect(t, "reinstated", got[len(got)-3:], []string{
		"HOST_REAPPEARED host-01 MISSING>ACTIVE passive_arp",
		"IP_ADDED host-01 >10.0.0.5 passive_arp",
		"HOST_CAP_REACHED - >3 passive_ipv4",
	})
	expect(t, "evicted after reinstatement", h.evictedHosts(), []string{"host-02 00:1b:1b:00:00:0b"})

	// B was evicted before the restart and is reinstated after it.
	h.restart()
	h.setCap(3)
	if h.c.st.contexts["eth0"].evicted != 1 || h.c.hostByID("host-02") != nil {
		t.Fatalf("evicted hosts after restart: %d", h.c.st.contexts["eth0"].evicted)
	}
	h.obs(52*time.Hour, observation.PassiveIPv4, "eth0", macB, "")
	if b := h.c.hostByID("host-02"); b == nil || b.presence != Active {
		t.Fatalf("B not reinstated: %+v", b)
	}
	if got := h.evictedHosts(); len(got) != 1 || !strings.HasPrefix(got[0], "host-0") || strings.HasPrefix(got[0], "host-02") {
		t.Errorf("evicted after B returned: %v", got)
	}
	h.flush()
}

// A cached fact about an evicted MAC holds the host again without making
// it present; a read failure drops the observation as unbound and evicts
// nothing for it.
func TestReinstateWithoutPresence(t *testing.T) {
	h := newHarness(t)
	h.setCap(1)
	h.obs(0, observation.PassiveIPv4, "eth0", macA, "")
	h.obs(25*time.Hour, observation.PassiveIPv4, "eth0", macB, "")
	h.obs(50*time.Hour, observation.PassiveIPv4, "eth0", macC, "")
	m, _ := net.ParseMAC(macA)
	o := observation.Observation{Time: h.at(75 * time.Hour), Source: observation.KernelNeighbor, Interface: "eth0", MAC: m,
		IP: netip.MustParseAddr("10.0.0.9"), NeighborState: "STALE"}
	h.sim.Set(o.Time)
	h.c.Handle(h.ctx, observation.Message{Observation: o})
	a := h.c.hostByID("host-01")
	if a == nil || a.presence != Missing {
		t.Fatalf("A: %+v", a)
	}

	// The reader is closed: B, evicted for A and committed, cannot be read
	// back. A, missing by now, stays held: the row is read before anything
	// is evicted.
	h.flush()
	if err := h.rd.Close(); err != nil {
		t.Fatal(err)
	}
	unbound, evicted := h.c.Unbound(), h.c.CapCounts()["eth0"].HostsEvicted
	h.c.Handle(h.ctx, observation.Message{Observation: observation.Observation{Time: h.at(100 * time.Hour), Source: observation.PassiveIPv4,
		Interface: "eth0", MAC: mustMAC(t, macB)}})
	if h.c.Unbound() != unbound+1 || h.c.hostByID("host-01") == nil || h.c.CapCounts()["eth0"].HostsEvicted != evicted {
		t.Errorf("unbound %d, A held %v, evicted %d", h.c.Unbound(), h.c.hostByID("host-01") != nil, h.c.CapCounts()["eth0"].HostsEvicted)
	}
}

// An evicted MAC that returns before the writer has committed its
// eviction is held again from memory, with what was written since (a
// description), and no commit is forced (NFR-PERF-2). Once committed, the
// rows are read through the reader and the memory is freed.
func TestReinstateWithoutCommit(t *testing.T) {
	h := newHarness(t)
	h.flushEvery = 10000 * time.Hour // no commit on a tick in this test
	h.restart()
	h.setCap(1)
	h.obs(0, observation.PassiveIPv4, "eth0", macA, "")
	h.flush()
	before := h.st.Commits()
	h.obs(25*time.Hour, observation.PassiveIPv4, "eth0", macB, "") // evicts A
	h.c.Handle(h.ctx, observation.Message{Operator: &observation.Operator{Time: h.at(26 * time.Hour), Kind: observation.OpHostDescribed,
		Actor: "op", HostID: "host-01", Description: "Pump 1"}})
	h.obs(50*time.Hour, observation.PassiveIPv4, "eth0", macA, "") // evicts B, A again
	h.obs(75*time.Hour, observation.PassiveIPv4, "eth0", macB, "") // B's row is not committed yet
	if got := h.st.Commits(); got != before {
		t.Errorf("commits while reinstating: %d, want %d", got, before)
	}
	if b := h.c.hostByID("host-02"); b == nil || b.presence != Active {
		t.Fatalf("B: %+v", b)
	}
	if n := len(h.c.st.pendingByID); n != 1 { // A
		t.Errorf("pending %d", n)
	}
	h.flush()
	expect(t, "evicted", h.evictedHosts(), []string{"host-01 " + macA})
	h.c.prunePending()
	if len(h.c.st.pendingByID) != 0 || len(h.c.st.pendingByMAC) != 0 || len(h.c.st.pendingQ) != 0 {
		t.Errorf("pending after commit: %d %d %d", len(h.c.st.pendingByID), len(h.c.st.pendingByMAC), len(h.c.st.pendingQ))
	}
	// Committed: A is read back through the reader, with its description.
	h.obs(100*time.Hour, observation.PassiveIPv4, "eth0", macA, "")
	if a := h.c.hostByID("host-01"); a == nil || a.description != "Pump 1" || a.presence != Active {
		t.Errorf("A after commit: %+v", a)
	}
	h.flush()
	got := h.query(`SELECT host_id || ' ' || coalesce(description, '') FROM hosts ORDER BY host_id`, scanText)
	expect(t, "hosts", got, []string{"host-01 Pump 1", "host-02 "})

	// A writer that has stopped: the description cannot be queued; it is
	// logged, not fatal.
	if err := h.st.Close(); err != nil {
		t.Fatal(err)
	}
	h.c.Handle(h.ctx, observation.Message{Operator: &observation.Operator{Time: h.at(101 * time.Hour), Kind: observation.OpHostDescribed,
		Actor: "op", HostID: "host-02", Description: "Pump 2"}})
	if p := h.c.st.pendingByID["host-02"]; p == nil || p.row.Description != "Pump 2" || p.batch.Load() != 0 {
		t.Errorf("pending after a stopped writer: %+v", p)
	}
}

func mustMAC(t *testing.T, s string) net.HardwareAddr {
	t.Helper()
	m, err := net.ParseMAC(s)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// An evicted host can still be described; the database holds it.
func TestDescribeEvictedHost(t *testing.T) {
	h := newHarness(t)
	h.setCap(1)
	h.obs(0, observation.PassiveIPv4, "eth0", macA, "")
	h.obs(25*time.Hour, observation.PassiveIPv4, "eth0", macB, "")
	describe := func(id, text string) {
		h.c.Handle(h.ctx, observation.Message{Operator: &observation.Operator{Time: h.at(26 * time.Hour), Kind: observation.OpHostDescribed,
			Actor: "op", HostID: id, Description: text}})
	}
	describe("host-02", "Pump 2") // held: described in memory
	describe("host-01", "Pump 3")
	describe("host-01", "Pump 3") // the same text: no event
	describe("nobody", "x")
	got := h.capEvents()
	expect(t, "events", got[len(got)-1:], []string{"HOST_DESCRIBED host-01 >Pump 3 operator"})
	if d := h.query(`SELECT description FROM hosts WHERE host_id = 'host-01'`, scanText); d[0] != "Pump 3" {
		t.Errorf("description %v", d)
	}
	h.flush()
	if err := h.rd.Close(); err != nil {
		t.Fatal(err)
	}
	describe("host-01", "Pump 4") // unreadable: ignored
	h.flush()
	if d := h.query(`SELECT description FROM hosts WHERE host_id = 'host-01'`, scanText); d[0] != "Pump 3" {
		t.Errorf("description after an unreadable row: %v", d)
	}
}

// The cap bounds the open DHCP leases per interface: a new client ends the
// lease with the oldest ACK.
func TestLeaseCap(t *testing.T) {
	h := newHarness(t)
	h.setCap(2)
	const srv = "00:1b:1b:00:00:99"
	ack := func(d time.Duration, chaddr, ip string) {
		h.dhcp(d, srv, "10.1.0.1", server(observation.DHCPAck, 1, chaddr, "10.1.0.1", ip, nil))
	}
	ack(0, macA, "10.1.0.10")
	ack(time.Minute, macB, "10.1.0.11")
	ack(2*time.Minute, macA, "10.1.0.10") // renewal: A is now newer than B
	ack(3*time.Minute, macC, "10.1.0.12")
	got := h.query(`SELECT chaddr || ' ' || coalesce(end_reason, 'open') FROM dhcp_leases ORDER BY id`, scanText)
	expect(t, "leases", got, []string{macA + " open", macB + " evicted", macC + " open"})
	if n := h.c.CapCounts()["eth1"].LeasesEvicted; n != 1 {
		t.Errorf("leases evicted %d", n)
	}
	// Loaded leases are queued too.
	h.restart()
	h.setCap(2)
	ack(4*time.Minute, macD, "10.1.0.13")
	got = h.query(`SELECT chaddr || ' ' || coalesce(end_reason, 'open') FROM dhcp_leases ORDER BY id`, scanText)
	expect(t, "leases after restart", got, []string{macA + " evicted", macB + " evicted", macC + " open", macD + " open"})
}

// A lease on the queue that is not its key's open lease is taken off the
// queue rather than ending the loop that makes room.
func TestLeaseQueueMismatch(t *testing.T) {
	h := newHarness(t)
	h.setCap(1)
	const srv = "00:1b:1b:00:00:99"
	h.dhcp(0, srv, "10.1.0.1", server(observation.DHCPAck, 1, macA, "10.1.0.1", "10.1.0.10", nil))
	n := h.c.st.contexts["eth1"]
	for k := range h.c.st.leases {
		delete(h.c.st.leases, k) // the queue still holds it
	}
	done := make(chan struct{})
	go func() {
		h.c.limitLeases(h.ctx, n, observation.Observation{Time: h.at(time.Minute)})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("limitLeases did not end")
	}
	if l := n.leaseQueue().Len(); l != 0 {
		t.Errorf("queue %d", l)
	}
}

// The cost of an observation that advances time does not grow with the
// number of hosts (ADR 0013): only the hosts whose timers are due are
// evaluated.
func TestAdvanceCostIndependentOfHosts(t *testing.T) {
	allocs := func(n int) float64 {
		h := newHarness(t)
		cfg := testConfig()
		cfg.Identity.MaxHostsPerInterface = n + 1
		h.c.SetConfig(cfg)
		for i := range n {
			mac := fmt.Sprintf("02:00:00:%02x:%02x:%02x", i>>16&0xff, i>>8&0xff, i&0xff)
			ip := netip.AddrFrom16([16]byte{0xfd, 15: byte(i), 14: byte(i >> 8), 13: 1}).String()
			h.obs(time.Duration(i)*time.Millisecond, observation.PassiveIPv6, "eth0", mac, ip)
		}
		h.flush()
		m := mustMAC(t, "02:00:00:00:00:00")
		at := h.at(time.Minute)
		return testing.AllocsPerRun(50, func() {
			at = at.Add(time.Second)
			h.c.Handle(h.ctx, observation.Message{Observation: observation.Observation{Time: at, Source: observation.PassiveIPv4,
				Interface: "eth0", MAC: m}})
		})
	}
	small, large := allocs(100), allocs(10000)
	if large > small+2 {
		t.Errorf("allocations per advancing observation: %v with 100 hosts, %v with 10000", small, large)
	}
}

// Timers follow a reload of the thresholds.
func TestTimersFollowReload(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveIPv4, "eth0", macA, "")
	cfg := testConfig()
	cfg.Presence = config.PresenceConfig{Active: config.Duration(time.Minute), Recent: config.Duration(2 * time.Minute),
		Stale: config.Duration(3 * time.Minute)}
	h.c.SetConfig(cfg)
	h.obs(4*time.Minute, observation.PassiveIPv4, "eth0", macB, "")
	if a := h.c.hostByID("host-01"); a.presence != Missing {
		t.Errorf("A after reload: %s", a.presence)
	}
}

// Evicting a MAC's host on one interface leaves its host on another, and
// ends every name record of the evicted host.
func TestEvictOneInterface(t *testing.T) {
	h := newHarness(t)
	h.setCap(1)
	h.obs(0, observation.PassiveIPv4, "eth0", macA, "")
	h.obs(0, observation.PassiveIPv4, "eth1", macA, "")
	h.named(time.Second, observation.PassiveMDNS, macA, "", observation.NameMDNS, "plc-a")
	h.named(2*time.Second, observation.PassiveMDNS, macA, "", observation.NameMDNS, "plc-a2")
	h.obs(25*time.Hour, observation.PassiveIPv4, "eth0", macB, "")
	if hs := h.c.st.hostsByMAC[macA]; len(hs) != 1 || hs[0].ctx.iface != "eth1" {
		t.Errorf("hosts of A: %v", hs)
	}
	got := h.query(`SELECT name || ' ' || end_reason FROM name_records ORDER BY id`, scanText)
	expect(t, "names", got, []string{"plc-a " + endHostEvicted, "plc-a2 " + endHostEvicted})
}
