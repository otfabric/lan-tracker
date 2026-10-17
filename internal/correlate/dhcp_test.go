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

const (
	dhcpA  = "00:15:5d:00:00:02" // the site's DHCP server
	dhcpB  = "00:15:5d:00:00:03" // its replacement, or a spoofer
	relayR = "00:00:5e:00:01:01" // a router relaying for a remote server
	rogue  = "02:00:00:00:00:66"
)

// reply feeds the sender of a DHCP server reply on eth1, with the message's
// fields as key/value pairs: dhcp (the type), server_id, relay (giaddr),
// router, dns (comma lists), subnet_mask and, for the ACK of an INFORM,
// config_only. An ACK otherwise assigns an address; the message has no
// client, so only the server side is recorded.
func (h *harness) reply(d time.Duration, mac, ip string, kv ...string) {
	h.t.Helper()
	m, err := net.ParseMAC(mac)
	if err != nil {
		h.t.Fatal(err)
	}
	msg := &observation.DHCPMessage{Op: 2, YIAddr: netip.IPv4Unspecified()}
	list := func(v string) []netip.Addr {
		var out []netip.Addr
		for _, a := range strings.Split(v, ",") {
			out = append(out, netip.MustParseAddr(a))
		}
		return out
	}
	configOnly := false
	for i := 0; i+1 < len(kv); i += 2 {
		switch v := kv[i+1]; kv[i] {
		case "dhcp":
			msg.Type = v
		case "server_id":
			msg.ServerID = netip.MustParseAddr(v)
		case "relay":
			msg.GIAddr = netip.MustParseAddr(v)
		case "router":
			msg.Routers = list(v)
		case "dns":
			msg.DNS = list(v)
		case "subnet_mask":
			msg.SubnetMask = netip.MustParseAddr(v)
		case "config_only":
			configOnly = true
		default:
			h.t.Fatalf("reply: unknown key %q", kv[i])
		}
	}
	if msg.Type == observation.DHCPAck && !configOnly {
		msg.YIAddr = netip.MustParseAddr("10.1.0.200")
	}
	o := observation.Observation{Time: h.at(d), Source: observation.PassiveDHCPServer, Interface: "eth1", MAC: m, DHCP: msg}
	if ip != "" {
		o.IP = netip.MustParseAddr(ip)
	}
	h.sim.Set(o.Time)
	h.c.Handle(h.ctx, observation.Message{Observation: o})
}

// allow sets eth1's DHCP allowlist: nil removes it, empty expects none.
func (h *harness) allow(servers []string) {
	cfg := testConfig()
	if servers != nil {
		list := []netip.Addr{}
		for _, s := range servers {
			list = append(list, netip.MustParseAddr(s))
		}
		cfg.Interfaces[1].DHCP.Servers = &list
	}
	h.c.SetConfig(cfg)
}

// dhcpEvents lists the DHCP server events.
func (h *harness) dhcpEvents() []string {
	var out []string
	for _, e := range h.events() {
		if strings.Contains(e, " DHCP_") {
			out = append(out, e)
		}
	}
	return out
}

// servers lists "server_id relay mac ip status config" per row.
func (h *harness) servers() []string {
	return h.query(`SELECT server_id, relay, mac, ip, status, config_json, coalesce(host_id, '-') FROM dhcp_servers ORDER BY id`,
		func(r *sql.Rows) string {
			var id, relay, mac, ip, status, cfg, host string
			if err := r.Scan(&id, &relay, &mac, &ip, &status, &cfg, &host); err != nil {
				h.t.Fatal(err)
			}
			return fmt.Sprintf("%q %q %s %s %s %s %s", id, relay, mac, ip, status, cfg, host)
		})
}

func TestDHCPServersWithoutAllowlist(t *testing.T) {
	h := newHarness(t)
	h.reply(0, dhcpA, "10.1.0.2", "dhcp", "offer", "server_id", "10.1.0.2")
	h.reply(time.Minute, dhcpA, "10.1.0.2", "dhcp", "ack", "server_id", "10.1.0.2")
	h.reply(2*time.Minute, rogue, "10.1.0.66", "dhcp", "offer") // no identifier
	expect(t, "events", h.dhcpEvents(), []string{
		"0s DHCP_SERVER_DISCOVERED host-01 >10.1.0.2",
		"2m0s DHCP_SERVER_DISCOVERED host-02 >unknown",
	})
	expect(t, "servers", h.servers(), []string{
		`"10.1.0.2" "" 00:15:5d:00:00:02 10.1.0.2 unchecked {} host-01`,
		`"" "" 02:00:00:00:00:66 10.1.0.66 unchecked {} host-02`,
	})
	// Another sender without an identifier is another unknown server.
	h.reply(3*time.Minute, dhcpB, "10.1.0.3", "dhcp", "nak")
	if ev := h.dhcpEvents(); len(ev) != 3 || ev[2] != "3m0s DHCP_SERVER_DISCOVERED host-03 >unknown" {
		t.Errorf("events = %v", ev)
	}
	// A reply without a usable sender MAC says nothing about the server,
	// even when its IP has a holder.
	h.c.Handle(h.ctx, observation.Message{Observation: observation.Observation{Time: h.at(3*time.Minute + time.Second),
		Source: observation.PassiveDHCPServer, Interface: "eth1", IP: netip.MustParseAddr("10.1.0.2"),
		DHCP: &observation.DHCPMessage{Op: 2, Type: observation.DHCPOffer, ServerID: netip.MustParseAddr("10.1.0.9")}}})
	// Anything but a server reply is not a server.
	h.reply(4*time.Minute, dhcpB, "10.1.0.3", "dhcp", "discover", "server_id", "10.1.0.9")
	if n := len(h.servers()); n != 3 {
		t.Errorf("%d server rows after a discover", n)
	}
}

func TestDHCPServerAllowlist(t *testing.T) {
	h := newHarness(t)
	h.allow([]string{"10.1.0.2"})
	h.reply(0, dhcpA, "10.1.0.2", "dhcp", "ack", "server_id", "10.1.0.2")
	h.reply(time.Minute, rogue, "10.1.0.66", "dhcp", "offer", "server_id", "10.1.0.66")
	h.reply(2*time.Minute, dhcpB, "10.1.0.3", "dhcp", "offer") // identity unknown: never allowed
	// The allowlist changes on reload; a known server's verdict follows at
	// its next reply.
	h.allow([]string{"10.1.0.66"})
	h.reply(time.Hour, dhcpA, "10.1.0.2", "dhcp", "ack", "server_id", "10.1.0.2")
	h.reply(time.Hour+time.Minute, rogue, "10.1.0.66", "dhcp", "offer", "server_id", "10.1.0.66")
	h.allow([]string{})
	h.reply(2*time.Hour, dhcpA, "10.1.0.2", "dhcp", "ack", "server_id", "10.1.0.2")
	h.allow(nil)
	h.reply(3*time.Hour, rogue, "10.1.0.66", "dhcp", "offer", "server_id", "10.1.0.66")
	expect(t, "events", h.dhcpEvents(), []string{
		"0s DHCP_SERVER_DISCOVERED host-01 >10.1.0.2",
		"1m0s DHCP_SERVER_DISCOVERED host-02 >10.1.0.66",
		"1m0s DHCP_SERVER_UNEXPECTED host-02 >10.1.0.66",
		"2m0s DHCP_SERVER_DISCOVERED host-03 >unknown",
		"2m0s DHCP_SERVER_UNEXPECTED host-03 >unknown",
		"1h0m0s DHCP_SERVER_UNEXPECTED host-01 allowed>10.1.0.2",
	})
	expect(t, "servers", h.servers(), []string{
		`"10.1.0.2" "" 00:15:5d:00:00:02 10.1.0.2 unexpected {} host-01`,
		`"10.1.0.66" "" 02:00:00:00:00:66 10.1.0.66 unchecked {} host-02`,
		`"" "" 00:15:5d:00:00:03 10.1.0.3 unexpected {} host-03`,
	})
}

// A known server identity replying from another sender MAC is flagged; the
// earlier sender stays on record. Through a relay it is the same server,
// not a MAC change.
func TestDHCPServerMACChanged(t *testing.T) {
	h := newHarness(t)
	h.reply(0, dhcpA, "10.1.0.2", "dhcp", "ack", "server_id", "10.1.0.2")
	h.reply(time.Hour, dhcpB, "10.1.0.2", "dhcp", "ack", "server_id", "10.1.0.2")
	h.reply(2*time.Hour, dhcpA, "10.1.0.2", "dhcp", "ack", "server_id", "10.1.0.2")
	h.reply(3*time.Hour, relayR, "10.1.0.1", "dhcp", "offer", "server_id", "10.1.0.2", "relay", "10.1.0.1")
	h.reply(4*time.Hour, rogue, "10.1.0.1", "dhcp", "offer", "server_id", "10.1.0.2", "relay", "10.1.0.1")
	expect(t, "events", h.dhcpEvents(), []string{
		"0s DHCP_SERVER_DISCOVERED host-01 >10.1.0.2",
		"1h0m0s DHCP_SERVER_MAC_CHANGED host-02 00:15:5d:00:00:02>00:15:5d:00:00:03",
		"4h0m0s DHCP_SERVER_MAC_CHANGED host-04 00:00:5e:00:01:01>02:00:00:00:00:66",
	})
	related := h.query(`SELECT related_host_id FROM events WHERE type = 'DHCP_SERVER_MAC_CHANGED' ORDER BY id`, func(r *sql.Rows) string {
		var s string
		_ = r.Scan(&s)
		return s
	})
	expect(t, "related hosts", related, []string{"host-01", "host-03"})
	if n := len(h.servers()); n != 4 {
		t.Errorf("%d server rows, want 4: %v", n, h.servers())
	}
}

// A server row shows the configuration last offered or assigned; a
// setting left out is kept, and neither the ACK of an INFORM, a NAK nor
// older evidence changes it. Changes are compared per client, in its lease
// history, never across clients: the row raises no event.
func TestDHCPServerConfig(t *testing.T) {
	h := newHarness(t)
	id := []string{"server_id", "10.1.0.2"}
	with := func(kv ...string) []string { return append(append([]string{}, id...), kv...) }
	h.reply(0, dhcpA, "10.1.0.2", with("dhcp", "offer", "router", "10.1.0.1", "dns", "10.1.0.2", "subnet_mask", "255.255.255.0")...)
	h.reply(time.Hour, dhcpA, "10.1.0.2", with("dhcp", "ack", "router", "10.1.0.1")...)
	h.reply(2*time.Hour, dhcpA, "10.1.0.2", with("dhcp", "ack", "router", "10.1.0.254", "dns", "10.1.0.2,9.9.9.9")...)
	h.reply(3*time.Hour, dhcpA, "10.1.0.2", with("dhcp", "ack", "config_only", "true", "router", "10.1.0.99")...)
	h.reply(4*time.Hour, dhcpA, "10.1.0.2", with("dhcp", "nak")...)
	h.reply(90*time.Minute, dhcpA, "10.1.0.2", with("dhcp", "offer", "router", "10.1.0.77")...) // older than the row
	expect(t, "events", h.dhcpEvents(), []string{
		"0s DHCP_SERVER_DISCOVERED host-01 >10.1.0.2",
	})
	expect(t, "servers", h.servers(), []string{
		`"10.1.0.2" "" 00:15:5d:00:00:02 10.1.0.2 unchecked {"dns":"10.1.0.2,9.9.9.9","router":"10.1.0.254","subnet_mask":"255.255.255.0"} host-01`,
	})
	last := h.query(`SELECT last_seen FROM dhcp_servers`, func(r *sql.Rows) string {
		var v int64
		_ = r.Scan(&v)
		return time.UnixMilli(v).UTC().Sub(t0).String()
	})
	if last[0] != "4h0m0s" {
		t.Errorf("last_seen = %s", last[0])
	}
}

// The servers, their senders and what they advertised survive a restart.
func TestDHCPServersSurviveRestart(t *testing.T) {
	h := newHarness(t)
	h.reply(0, dhcpA, "10.1.0.2", "dhcp", "offer", "server_id", "10.1.0.2", "router", "10.1.0.1")
	h.restart()
	h.reply(time.Hour, dhcpA, "10.1.0.2", "dhcp", "offer", "server_id", "10.1.0.2", "router", "10.1.0.254")
	h.reply(2*time.Hour, dhcpB, "10.1.0.2", "dhcp", "offer", "server_id", "10.1.0.2")
	h.restart()
	h.reply(3*time.Hour, relayR, "", "dhcp", "offer", "server_id", "10.1.0.9")
	expect(t, "events", h.dhcpEvents(), []string{
		"0s DHCP_SERVER_DISCOVERED host-01 >10.1.0.2",
		"2h0m0s DHCP_SERVER_MAC_CHANGED host-02 00:15:5d:00:00:02>00:15:5d:00:00:03",
		"3h0m0s DHCP_SERVER_DISCOVERED host-03 >10.1.0.9",
	})
	rows := h.servers()
	if len(rows) != 3 || !strings.HasPrefix(rows[2], `"10.1.0.9" "" 00:00:5e:00:01:01  unchecked`) {
		t.Errorf("servers = %v", rows)
	}
}

// A lease ACK is layer-2 evidence like the client's own messages, so a
// lease outside the interface's subnets is recorded; the reply's sender is
// bound only on the link, like any IP source.
// A server's lease ACK is its statement about a client: it creates no
// host, binds no address and refreshes no presence (docs/DATA_MODEL.md §2,
// §5.6). A server's replies bind its own address when it is on-link.
func TestDHCPLeaseAndServerAddresses(t *testing.T) {
	h := newHarness(t)
	h.dhcp(0, dhcpA, "192.168.9.2", server(observation.DHCPAck, 1, plc, "192.168.9.2", "192.168.9.20", nil))
	h.reply(time.Minute, dhcpA, "192.168.9.2", "dhcp", "ack", "server_id", "192.168.9.2")
	h.reply(2*time.Minute, dhcpB, "10.1.0.2", "dhcp", "ack", "server_id", "10.1.0.2")
	expect(t, "bindings", h.bindings(), []string{"host-02 10.1.0.2 [2m0s,open) c=0"})
	expect(t, "servers", h.servers(), []string{
		`"192.168.9.2" "" 00:15:5d:00:00:02 192.168.9.2 unchecked {} host-01`,
		`"10.1.0.2" "" 00:15:5d:00:00:03 10.1.0.2 unchecked {} host-02`,
	})
	// A known client: the lease ACK still does not refresh it.
	h.obs(3*time.Minute, observation.PassiveARP, "eth1", plc, "10.1.0.50")
	h.dhcp(time.Hour, dhcpB, "10.1.0.2", server(observation.DHCPAck, 2, plc, "10.1.0.2", "10.1.0.51", nil))
	if got := h.query(`SELECT last_seen FROM hosts WHERE mac = '`+plc+`'`, scanText); got[0] != fmt.Sprint(ms(h.at(3*time.Minute))) {
		t.Errorf("the lease refreshed the client: last_seen %v", got)
	}
	if n := len(h.bindings()); n != 2 {
		t.Errorf("the lease bound an address: %v", h.bindings())
	}
}

func TestDHCPStatus(t *testing.T) {
	cfg := testConfig()
	none := []netip.Addr{}
	cfg.Interfaces[0].DHCP.Servers = &none
	for _, tt := range []struct{ iface, id, want string }{
		{"eth0", "10.0.0.2", dhcpUnexpected}, // nothing expected
		{"eth0", "", dhcpUnexpected},
		{"eth1", "10.1.0.2", dhcpUnchecked}, // no allowlist
		{"eth9", "10.1.0.2", dhcpUnchecked}, // not configured (any more)
	} {
		if got := dhcpStatus(cfg, tt.iface, tt.id); got != tt.want {
			t.Errorf("dhcpStatus(%s, %q) = %s, want %s", tt.iface, tt.id, got, tt.want)
		}
	}
	if (dhcpKey{serverID: "10.1.0.2", relay: "10.1.0.1"}).identity() != "10.1.0.2 via 10.1.0.1" {
		t.Error("identity through a relay")
	}
}
