package correlate

import (
	"database/sql"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/observation"
)

// named feeds an observation carrying a hostname at t0+d.
func (h *harness) named(d time.Duration, src observation.Source, mac, ip string, typ observation.NameType, name string) {
	h.t.Helper()
	o := observation.Observation{Time: h.at(d), Source: src, Interface: "eth0", Hostname: name, NameType: typ}
	if mac != "" {
		o.MAC, _ = net.ParseMAC(mac)
	}
	if ip != "" {
		o.IP = netip.MustParseAddr(ip)
	}
	h.sim.Set(o.Time)
	h.c.Handle(h.ctx, observation.Message{Observation: o})
}

// names lists "host type:name source [first,ended)" for every name record;
// an unlinked record's host is "-".
func (h *harness) names() []string {
	return h.query(`SELECT coalesce(host_id, '-'), name_type, name, source, first_seen, coalesce(ended_at, -1), coalesce(end_reason, '')
		FROM name_records ORDER BY id`,
		func(r *sql.Rows) string {
			var host, typ, name, src, reason string
			var first, ended int64
			if err := r.Scan(&host, &typ, &name, &src, &first, &ended, &reason); err != nil {
				h.t.Fatal(err)
			}
			end := "open"
			if ended >= 0 {
				end = time.UnixMilli(ended).UTC().Sub(t0).String()
			}
			s := fmt.Sprintf("%s %s:%s %s [%s,%s)", host, typ, name, src, time.UnixMilli(first).UTC().Sub(t0), end)
			if reason != "" {
				s += " " + reason
			}
			return s
		})
}

// mdns feeds an mDNS response from mac at ip with records at t0+d.
func (h *harness) mdns(d time.Duration, mac, ip string, records ...observation.Record) {
	h.t.Helper()
	m, _ := net.ParseMAC(mac)
	o := observation.Observation{Time: h.at(d), Source: observation.PassiveMDNS, Interface: "eth0", MAC: m,
		IP: netip.MustParseAddr(ip), Records: records}
	h.sim.Set(o.Time)
	h.c.Handle(h.ctx, observation.Message{Observation: o})
}

func aRecord(name, ip string, ttl uint32, flush bool) observation.Record {
	return observation.Record{Name: name, Type: observation.RecordA, Addr: netip.MustParseAddr(ip), TTL: observation.TTLOf(ttl), Flush: flush}
}

func (h *harness) preferred() []string {
	return h.query(`SELECT host_id || ' ' || coalesce(preferred_name, '-') FROM hosts ORDER BY host_id`, func(r *sql.Rows) string {
		var s string
		if err := r.Scan(&s); err != nil {
			h.t.Fatal(err)
		}
		return s
	})
}

// A host has several names of a type: a new DHCP name does not end the
// old one (a rename is not established by a second name), and each new
// name is HOSTNAME_ADDED.
func TestNames(t *testing.T) {
	h := newHarness(t)
	h.named(0, observation.PassiveDHCP, macA, "", observation.NameDHCP, "plc-a")
	h.named(time.Minute, observation.PassiveDHCP, macA, "", observation.NameDHCP, "plc-a") // confirmation
	h.named(2*time.Minute, observation.PassiveLLDP, macA, "", observation.NameLLDP, "PLC A")
	expect(t, "preferred after dhcp and lldp", h.preferred(), []string{"host-01 plc-a"})
	h.named(3*time.Minute, observation.PassiveMDNS, macA, "10.0.0.5", observation.NameMDNS, "plc-a.local")
	expect(t, "mdns preferred", h.preferred(), []string{"host-01 plc-a.local"})
	h.named(time.Hour, observation.PassiveDHCP, macA, "", observation.NameDHCP, "plc-a2")
	h.named(30*time.Minute, observation.PassiveDHCP, macA, "", observation.NameDHCP, "plc-a") // late: refreshes nothing new
	// Invalid name types are ignored.
	h.named(2*time.Hour, observation.PassiveDHCP, macA, "", observation.NameType("wins"), "PLC")

	expect(t, "names", h.names(), []string{
		"host-01 dhcp:plc-a passive_dhcp [0s,open)",
		"host-01 lldp:PLC A passive_lldp [2m0s,open)",
		"host-01 mdns:plc-a.local passive_mdns [3m0s,open)",
		"host-01 dhcp:plc-a2 passive_dhcp [1h0m0s,open)",
	})
	expect(t, "events", hostEvents(h.events()), []string{
		"0s HOST_DISCOVERED host-01 >00:1b:1b:00:00:0a",
		"0s HOSTNAME_ADDED host-01 >dhcp:plc-a",
		"2m0s HOSTNAME_ADDED host-01 >lldp:PLC A",
		"3m0s IP_ADDED host-01 >10.0.0.5",
		"3m0s HOSTNAME_ADDED host-01 >mdns:plc-a.local",
		"1h0m0s HOSTNAME_ADDED host-01 >dhcp:plc-a2",
	})

	// Names survive a restart: confirmations refresh, nothing is reopened.
	h.restart()
	h.named(50*time.Minute, observation.PassiveDHCP, macA, "", observation.NameDHCP, "plc-a")
	h.named(2*time.Hour, observation.PassiveMDNS, macA, "10.0.0.5", observation.NameMDNS, "plc-a.local")
	if n := len(h.names()); n != 4 {
		t.Errorf("after restart: %d name rows, want 4: %v", n, h.names())
	}
	expect(t, "preferred after restart", h.preferred(), []string{"host-01 plc-a.local"})
	if n := len(hostEvents(h.events())); n != 6 {
		t.Errorf("events after restart: %v", hostEvents(h.events()))
	}
}

// Names are last-known facts: time alone never closes them, whether the
// host stays visible (DHCP and mDNS are often seen only at boot on a
// switched port) or goes MISSING, and a TTL running out only marks a
// record expired when it is read.
func TestNamesOutliveNameExpiry(t *testing.T) {
	h := newHarness(t)
	week := 168 * time.Hour
	h.named(0, observation.PassiveMDNS, macA, "10.0.0.5", observation.NameMDNS, "plc-a.local")
	h.named(0, observation.PassiveDHCP, macA, "10.0.0.5", observation.NameDHCP, "plc-a")
	h.named(0, observation.PassiveDHCP, macB, "10.0.0.6", observation.NameDHCP, "hmi")
	h.mdns(time.Second, macA, "10.0.0.5", aRecord("plc-a-ttl.local", "10.0.0.5", 120, false))
	// A stays visible over ARP for three weeks without naming traffic; B
	// goes silent and becomes MISSING.
	for d := time.Hour; d <= 3*week; d += time.Hour {
		h.obs(d, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	}
	expect(t, "names", h.names(), []string{
		"host-01 mdns:plc-a.local passive_mdns [0s,open)",
		"host-01 dhcp:plc-a passive_dhcp [0s,open)",
		"host-02 dhcp:hmi passive_dhcp [0s,open)",
		"host-01 mdns:plc-a-ttl.local passive_mdns [1s,open)",
	})
	expect(t, "preferred names", h.preferred(), []string{"host-01 plc-a.local", "host-02 hmi"})
	for _, e := range h.events() {
		if strings.Contains(e, "HOSTNAME_REMOVED") {
			t.Errorf("name event without new naming evidence: %s", e)
		}
	}
	if got := h.query(`SELECT presence FROM hosts WHERE host_id = 'host-02'`, scanText); got[0] != "MISSING" {
		t.Fatalf("host-02 presence = %v, want MISSING", got)
	}
	if got := h.query(`SELECT ttl_seconds || ' ' || (expires_at - first_seen) FROM name_records WHERE name = 'plc-a-ttl.local'`, scanText); got[0] != "120 120000" {
		t.Errorf("ttl and expiry = %v", got)
	}
}

// An mDNS proxy (a reflector, a sleep proxy, a gateway answering for
// others) advertises addresses that are not its own: each record is linked
// to the one MAC holding the address, or to none, and never binds an
// address to the advertiser.
func TestMDNSProxyAdvertisesAnotherDevice(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macB, "10.0.0.6")
	h.mdns(time.Minute, macC, "10.0.0.7",
		aRecord("plc-b.local", "10.0.0.6", 120, true), aRecord("ghost.local", "10.0.0.99", 120, true), aRecord("proxy.local", "10.0.0.7", 120, true))
	expect(t, "bindings", h.bindings(), []string{
		"host-01 10.0.0.6 [0s,open) c=0",
		"host-02 10.0.0.7 [1m0s,open) c=0", // the proxy's own source address only
	})
	expect(t, "names", h.names(), []string{
		"host-01 mdns:plc-b.local passive_mdns [1m0s,open)",
		"- mdns:ghost.local passive_mdns [1m0s,open)",
		"host-02 mdns:proxy.local passive_mdns [1m0s,open)",
	})
	got := h.query(`SELECT name || ' ' || link || ' ' || advertiser_mac FROM name_records ORDER BY id`, scanText)
	expect(t, "advertiser and link", got, []string{
		"plc-b.local address " + macC, "ghost.local none " + macC, "proxy.local address " + macC,
	})
	expect(t, "preferred", h.preferred(), []string{"host-01 plc-b.local", "host-02 proxy.local"})
}

// Aliases are normal; an mDNS goodbye ends a record one second later, a
// cache-flush record ends the older records of the name and type, and the
// host's last record of a name ending is HOSTNAME_REMOVED.
func TestMDNSAliasesGoodbyeAndCacheFlush(t *testing.T) {
	h := newHarness(t)
	h.mdns(0, macA, "10.0.0.5", aRecord("plc-a.local", "10.0.0.5", 120, true), aRecord("alias.local", "10.0.0.5", 120, true))
	h.mdns(time.Minute, macA, "10.0.0.5", observation.Record{Name: "alias.local", Type: observation.RecordA,
		Addr: netip.MustParseAddr("10.0.0.5"), TTL: observation.TTLOf(0)})
	// plc-a.local moves to another address of A: the flush ends the old one.
	h.obs(2*time.Minute, observation.PassiveARP, "eth0", macA, "10.0.0.15")
	h.mdns(3*time.Minute, macA, "10.0.0.15", aRecord("plc-a.local", "10.0.0.15", 120, true))
	expect(t, "names", h.names(), []string{
		"host-01 mdns:plc-a.local passive_mdns [0s,3m0s) cache_flush",
		"host-01 mdns:alias.local passive_mdns [0s,1m1s) goodbye",
		"host-01 mdns:plc-a.local passive_mdns [3m0s,open)",
	})
	expect(t, "events", hostEvents(h.events()), []string{
		"0s HOST_DISCOVERED host-01 >00:1b:1b:00:00:0a",
		"0s HOSTNAME_ADDED host-01 >mdns:plc-a.local",
		"0s HOSTNAME_ADDED host-01 >mdns:alias.local",
		"1m1s HOSTNAME_REMOVED host-01 mdns:alias.local>",
		"2m0s IP_ADDED host-01 >10.0.0.15",
	})
}

// A DNS answer names the host that holds the address, without making it
// present or confirming the binding; an answer for an address no MAC holds
// is kept as an unlinked record.
func TestDNSNamesAttachWithoutPresence(t *testing.T) {
	h := newHarness(t)
	h.obs(0, observation.PassiveARP, "eth0", macA, "10.0.0.5")
	h.named(time.Hour, observation.PassiveDNS, "", "10.0.0.5", observation.NameDNSPTR, "plc-a.plant.example")
	h.named(time.Hour, observation.PassiveDNS, "", "10.0.0.99", observation.NameDNSPTR, "nobody.plant.example")
	expect(t, "names", h.names(), []string{
		"host-01 dns_ptr:plc-a.plant.example passive_dns [1h0m0s,open)",
		"- dns_ptr:nobody.plant.example passive_dns [1h0m0s,open)",
	})
	expect(t, "host not refreshed", h.query(`SELECT presence || ' ' || last_seen FROM hosts`, func(r *sql.Rows) string {
		var s string
		_ = r.Scan(&s)
		return s
	}), []string{fmt.Sprintf("STALE %d", t0.UnixMilli())})
	expect(t, "binding not refreshed", h.query(`SELECT last_seen FROM addresses`, func(r *sql.Rows) string {
		var v int64
		_ = r.Scan(&v)
		return fmt.Sprint(v)
	}), []string{fmt.Sprint(t0.UnixMilli())})
	if h.c.Unbound() != 1 {
		t.Errorf("unbound = %d, want 1 (the name for an unknown address)", h.c.Unbound())
	}
}
