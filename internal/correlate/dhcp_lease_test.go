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

// dhcp feeds a decoded DHCP message sent from mac (and ip, if any) on eth1.
func (h *harness) dhcp(d time.Duration, mac, ip string, m observation.DHCPMessage) {
	h.t.Helper()
	sender, err := net.ParseMAC(mac)
	if err != nil {
		h.t.Fatal(err)
	}
	o := observation.Observation{Time: h.at(d), Source: observation.PassiveDHCP, Interface: "eth1", MAC: sender, DHCP: &m}
	if m.Op == 2 {
		o.Source = observation.PassiveDHCPServer
	}
	if ip != "" {
		o.IP = netip.MustParseAddr(ip)
	}
	h.sim.Set(o.Time)
	h.c.Handle(h.ctx, observation.Message{Observation: o})
}

func addr(s string) netip.Addr { return netip.MustParseAddr(s) }

func addrs(s ...string) []netip.Addr {
	out := make([]netip.Addr, len(s))
	for i, a := range s {
		out[i] = addr(a)
	}
	return out
}

func secs(v uint32) *uint32 { return &v }

// client is a client's message; server a server's reply carrying the given
// options (Present is filled from the fields set).
func client(typ string, xid uint32, chaddr string) observation.DHCPMessage {
	return observation.DHCPMessage{Op: 1, Type: typ, XID: xid, CHAddr: chaddr}
}

func server(typ string, xid uint32, chaddr, serverID, yiaddr string, set func(*observation.DHCPMessage)) observation.DHCPMessage {
	m := observation.DHCPMessage{Op: 2, Type: typ, XID: xid, CHAddr: chaddr, YIAddr: netip.IPv4Unspecified()}
	if serverID != "" {
		m.ServerID = addr(serverID)
		m.Present = append(m.Present, 54)
	}
	if yiaddr != "" {
		m.YIAddr = addr(yiaddr)
	}
	if set != nil {
		set(&m)
	}
	if m.SubnetMask.IsValid() {
		m.Present = append(m.Present, 1)
	}
	if m.Routers != nil {
		m.Present = append(m.Present, 3)
	}
	if m.DNS != nil {
		m.Present = append(m.Present, 6)
	}
	if m.Routes != nil {
		m.Present = append(m.Present, 121)
	}
	return m
}

// exchanges lists "xid chaddr messages outcome visibility selected
// requested offers relayed config lease" per row.
func (h *harness) exchanges() []string {
	return h.query(`SELECT xid, chaddr, messages, outcome, visibility, coalesce(selected_server, '-'), coalesce(requested_ip, '-'),
		json_array_length(offers_json), relayed, coalesce(config_json, '-'), coalesce(lease_id, 0) FROM dhcp_exchanges ORDER BY id`,
		func(r *sql.Rows) string {
			var xid, offers, relayed, lease int64
			var chaddr, msgs, out, vis, sel, req, cfg string
			if err := r.Scan(&xid, &chaddr, &msgs, &out, &vis, &sel, &req, &offers, &relayed, &cfg, &lease); err != nil {
				h.t.Fatal(err)
			}
			return fmt.Sprintf("%d %s [%s] %s %s sel=%s req=%s offers=%d relayed=%d cfg=%s lease=%d",
				xid, chaddr, msgs, out, vis, sel, req, offers, relayed, cfg, lease)
		})
}

// leases lists one line per lease row.
func (h *harness) leases() []string {
	at := func(v int64) string { return time.UnixMilli(v).UTC().Sub(t0).String() }
	return h.query(`SELECT chaddr, ip, server_id, sender_mac, relay, coalesce(relay_circuit, '-'), acks, coalesce(lease_seconds, '?'),
		coalesce(t1_seconds, '?'), coalesce(t2_seconds, '?'), t_derived, partial, config_json, first_ack, last_ack,
		coalesce(expires_at, -1), coalesce(ended_at, -1), coalesce(end_reason, 'open'), coalesce(host_id, '-') FROM dhcp_leases ORDER BY id`,
		func(r *sql.Rows) string {
			var chaddr, ip, sid, sender, relay, circuit, lease, t1, t2, cfg, reason, host string
			var acks, derived, partial, first, last, expires, ended int64
			if err := r.Scan(&chaddr, &ip, &sid, &sender, &relay, &circuit, &acks, &lease, &t1, &t2, &derived, &partial, &cfg,
				&first, &last, &expires, &ended, &reason, &host); err != nil {
				h.t.Fatal(err)
			}
			exp, end := "-", reason
			if expires >= 0 {
				exp = at(expires)
			}
			if ended >= 0 {
				end = reason + "@" + at(ended)
			}
			return fmt.Sprintf("%s %s from %s/%s relay=%q/%s acks=%d lease=%s t=%s/%s derived=%d partial=%d %s..%s exp=%s %s %s %s",
				chaddr, ip, sid, sender, relay, circuit, acks, lease, t1, t2, derived, partial, at(first), at(last), exp, end, host, cfg)
		})
}

// A full exchange, two offers, the client's choice and its renewals: one
// lease row per address and acknowledged configuration, and events only
// for what changed.
func TestDHCPLeaseHistory(t *testing.T) {
	h := newHarness(t)
	opts := func(dns ...string) func(*observation.DHCPMessage) {
		return func(m *observation.DHCPMessage) {
			m.SubnetMask, m.Routers, m.Lease = addr("255.255.255.0"), addrs("10.1.0.1"), secs(3600)
			if dns != nil {
				m.DNS = addrs(dns...)
			}
		}
	}
	h.dhcp(0, plc, "", client(observation.DHCPDiscover, 1, plc))
	h.dhcp(500*time.Millisecond, plc, "", client(observation.DHCPDiscover, 1, plc)) // retransmitted
	h.dhcp(time.Second, dhcpA, "10.1.0.2", server(observation.DHCPOffer, 1, plc, "10.1.0.2", "10.1.0.50", opts("10.1.0.2")))
	h.dhcp(time.Second, dhcpB, "10.1.0.3", server(observation.DHCPOffer, 1, plc, "10.1.0.3", "10.1.0.60", nil))
	req := client(observation.DHCPRequest, 1, plc)
	req.ServerID, req.RequestedIP = addr("10.1.0.2"), addr("10.1.0.50")
	h.dhcp(3*time.Second, plc, "", req)
	h.dhcp(4*time.Second, dhcpA, "10.1.0.2", server(observation.DHCPAck, 1, plc, "10.1.0.2", "10.1.0.50", opts("10.1.0.2")))
	// A unicast renewal: only the ACK is seen. The same address and
	// configuration extends the row.
	h.dhcp(30*time.Minute, dhcpA, "10.1.0.2", server(observation.DHCPAck, 2, plc, "10.1.0.2", "10.1.0.50", opts("10.1.0.2")))
	h.restart()
	// An option the ACK leaves out is unknown, not changed.
	h.dhcp(time.Hour, dhcpA, "10.1.0.2", server(observation.DHCPAck, 3, plc, "10.1.0.2", "10.1.0.50", opts()))
	h.dhcp(2*time.Hour, dhcpA, "10.1.0.2", server(observation.DHCPAck, 4, plc, "10.1.0.2", "10.1.0.50", opts("10.1.0.2", "9.9.9.9")))
	h.dhcp(3*time.Hour, dhcpA, "10.1.0.2", server(observation.DHCPAck, 5, plc, "10.1.0.2", "10.1.0.51", opts("10.1.0.2", "9.9.9.9")))
	rel := client(observation.DHCPRelease, 6, plc)
	rel.CIAddr, rel.ServerID = addr("10.1.0.51"), addr("10.1.0.2")
	h.dhcp(4*time.Hour, plc, "", rel)
	h.dhcp(5*time.Hour, dhcpA, "10.1.0.2", server(observation.DHCPAck, 7, plc, "10.1.0.2", "10.1.0.52", func(m *observation.DHCPMessage) {
		m.Lease = secs(^uint32(0))
	}))
	dec := client(observation.DHCPDecline, 8, plc)
	dec.RequestedIP = addr("10.1.0.52")
	h.dhcp(6*time.Hour, plc, "", dec)

	expect(t, "events", h.dhcpEvents(), []string{
		"1s DHCP_SERVER_DISCOVERED host-02 >10.1.0.2",
		"1s DHCP_SERVER_DISCOVERED host-03 >10.1.0.3",
		"4s DHCP_LEASE_ASSIGNED host-01 >10.1.0.50",
		"2h0m0s DHCP_CLIENT_CONFIG_CHANGED host-01 dns=10.1.0.2>dns=10.1.0.2,9.9.9.9",
		"3h0m0s DHCP_LEASE_ASSIGNED host-01 10.1.0.50>10.1.0.51",
		"4h0m0s DHCP_LEASE_RELEASED host-01 10.1.0.51>",
		"5h0m0s DHCP_LEASE_ASSIGNED host-01 >10.1.0.52",
		"6h0m0s DHCP_LEASE_DECLINED host-01 10.1.0.52>",
	})
	cfg := `{"dns":"10.1.0.2","gateway":"10.1.0.1","gateway_source":"option_3","prefix_length":24,"routers":"10.1.0.1","subnet_mask":"255.255.255.0"}`
	cfg2 := strings.Replace(cfg, `"dns":"10.1.0.2"`, `"dns":"10.1.0.2,9.9.9.9"`, 1)
	expect(t, "leases", h.leases(), []string{
		`00:1b:1b:aa:bb:01 10.1.0.50 from 10.1.0.2/00:15:5d:00:00:02 relay=""/- acks=3 lease=3600 t=1800/3150 derived=1 partial=0 4s..1h0m0s exp=2h0m0s superseded@2h0m0s host-01 ` + cfg,
		`00:1b:1b:aa:bb:01 10.1.0.50 from 10.1.0.2/00:15:5d:00:00:02 relay=""/- acks=1 lease=3600 t=1800/3150 derived=1 partial=1 2h0m0s..2h0m0s exp=3h0m0s superseded@3h0m0s host-01 ` + cfg2,
		`00:1b:1b:aa:bb:01 10.1.0.51 from 10.1.0.2/00:15:5d:00:00:02 relay=""/- acks=1 lease=3600 t=1800/3150 derived=1 partial=1 3h0m0s..3h0m0s exp=4h0m0s released@4h0m0s host-01 ` + cfg2,
		`00:1b:1b:aa:bb:01 10.1.0.52 from 10.1.0.2/00:15:5d:00:00:02 relay=""/- acks=1 lease=-1 t=?/? derived=0 partial=1 5h0m0s..5h0m0s exp=- declined@6h0m0s host-01 {}`,
	})
	ex := h.exchanges()
	if len(ex) != 8 || ex[0] != "1 00:1b:1b:aa:bb:01 [discover×2 offer(10.1.0.2 10.1.0.50) offer(10.1.0.3 10.1.0.60) request(10.1.0.2) "+
		"ack(10.1.0.2 10.1.0.50)] ack complete sel=10.1.0.2 req=10.1.0.50 offers=2 relayed=0 cfg=- lease=1" ||
		ex[1] != "2 00:1b:1b:aa:bb:01 [ack(10.1.0.2 10.1.0.50)] ack partial sel=- req=- offers=0 relayed=0 cfg=- lease=1" ||
		ex[7] != "8 00:1b:1b:aa:bb:01 [decline] decline complete sel=- req=- offers=0 relayed=0 cfg=- lease=0" {
		t.Errorf("exchanges:\n%s", strings.Join(ex, "\n"))
	}
	// The ACKs bound no address to the client and refreshed nothing.
	if b := h.bindings(); len(b) != 2 || strings.Contains(strings.Join(b, " "), "host-01") {
		t.Errorf("bindings = %v", b)
	}
}

// Exchanges seen in part: an offer nobody took, a REQUEST whose answer was
// not seen, an INFORM, a NAK, a relayed ACK for a client on another link
// and a route-defined gateway.
func TestDHCPExchangeOutcomes(t *testing.T) {
	h := newHarness(t)
	h.dhcp(0, macA, "", client(observation.DHCPDiscover, 10, macA))
	h.dhcp(time.Second, dhcpA, "10.1.0.2", server(observation.DHCPOffer, 10, macA, "10.1.0.2", "10.1.0.40", nil))
	h.dhcp(2*time.Second, macB, "", client(observation.DHCPRequest, 11, macB))  // broadcast, never answered
	h.dhcp(2*time.Second, macC, "", client(observation.DHCPDiscover, 17, macC)) // open across a restart
	h.restart()
	inf := client(observation.DHCPInform, 12, macC)
	inf.CIAddr = addr("10.1.0.70")
	h.dhcp(3*time.Second, macC, "10.1.0.70", inf)
	h.dhcp(4*time.Second, dhcpA, "10.1.0.2", server(observation.DHCPAck, 12, macC, "10.1.0.2", "", func(m *observation.DHCPMessage) {
		m.CIAddr, m.DNS = addr("10.1.0.70"), addrs("10.1.0.2")
	}))
	req := client(observation.DHCPRequest, 13, macA)
	req.RequestedIP = addr("10.1.0.80")
	h.dhcp(5*time.Second, macA, "", req)
	h.dhcp(6*time.Second, dhcpA, "10.1.0.2", server(observation.DHCPNak, 13, macA, "10.1.0.2", "", nil))
	const remote = "00:1b:1b:00:00:0d"
	h.dhcp(7*time.Second, relayR, "10.1.0.1", server(observation.DHCPAck, 14, remote, "10.9.9.2", "10.9.0.50", func(m *observation.DHCPMessage) {
		m.GIAddr, m.RelayAgent, m.Lease, m.T1, m.T2 = addr("10.9.0.1"), &observation.DHCPRelay{CircuitID: "01", RemoteID: "aa"}, secs(600), secs(200), secs(500)
		m.Present = append(m.Present, 82)
	}))
	h.dhcp(8*time.Second, dhcpA, "10.1.0.2", server(observation.DHCPAck, 15, macB, "10.1.0.2", "10.1.0.90", func(m *observation.DHCPMessage) {
		m.Routers = addrs("10.1.0.1")
		m.Routes = []observation.DHCPRoute{{Destination: netip.MustParsePrefix("0.0.0.0/0"), Router: addr("10.1.0.254")}}
	}))
	h.dhcp(9*time.Second, macA, "", client(observation.DHCPDecline, 16, macA)) // without option 50
	// Anything later closes the exchanges idle for a minute.
	h.obs(5*time.Minute, observation.PassiveARP, "eth1", macA, "10.1.0.40")

	expect(t, "exchanges", h.exchanges(), []string{
		"10 00:1b:1b:00:00:0a [discover offer(10.1.0.2 10.1.0.40)] offered complete sel=- req=- offers=1 relayed=0 cfg=- lease=0",
		"11 00:1b:1b:00:00:0b [request] incomplete partial sel=- req=- offers=0 relayed=0 cfg=- lease=0",
		"17 00:1b:1b:00:00:0c [discover] incomplete partial sel=- req=- offers=0 relayed=0 cfg=- lease=0",
		`12 00:1b:1b:00:00:0c [inform ack(10.1.0.2)] inform_ack complete sel=- req=- offers=0 relayed=0 cfg={"dns":"10.1.0.2"} lease=0`,
		"13 00:1b:1b:00:00:0a [request nak(10.1.0.2)] nak complete sel=- req=10.1.0.80 offers=0 relayed=0 cfg=- lease=0",
		"14 00:1b:1b:00:00:0d [ack(10.9.9.2 10.9.0.50)] ack partial sel=- req=- offers=0 relayed=1 cfg=- lease=1",
		"15 00:1b:1b:00:00:0b [ack(10.1.0.2 10.1.0.90)] ack partial sel=- req=- offers=0 relayed=0 cfg=- lease=2",
		"16 00:1b:1b:00:00:0a [decline] decline complete sel=- req=- offers=0 relayed=0 cfg=- lease=0",
	})
	expect(t, "leases", h.leases(), []string{
		`00:1b:1b:00:00:0d 10.9.0.50 from 10.9.9.2/00:00:5e:00:01:01 relay="10.9.0.1"/01 acks=1 lease=600 t=200/500 derived=0 partial=1 7s..7s exp=10m7s open - ` +
			`{"relay_circuit":"01","relay_remote":"aa"}`,
		`00:1b:1b:00:00:0b 10.1.0.90 from 10.1.0.2/00:15:5d:00:00:02 relay=""/- acks=1 lease=? t=?/? derived=0 partial=1 8s..8s exp=- open host-03 ` +
			`{"classless_routes":"0.0.0.0/0 via 10.1.0.254","gateway":"10.1.0.254","gateway_source":"option_121","routers":"10.1.0.1"}`,
	})
	if ev := strings.Join(h.dhcpEvents(), "\n"); !strings.Contains(ev, "6s DHCP_LEASE_NAK host-01 10.1.0.80>nak from 10.1.0.2") ||
		!strings.Contains(ev, "9s DHCP_LEASE_DECLINED host-01 >") {
		t.Errorf("events = %s", ev)
	}
	// The relayed client is on another link: no host for it.
	if n := h.query(`SELECT count(*) FROM hosts WHERE mac = '`+remote+`'`, scanText); n[0] != "0" {
		t.Errorf("a relayed ACK created its client")
	}
}

// A context keeps at most maxExchanges open: the oldest are closed first.
func TestDHCPExchangeLimit(t *testing.T) {
	h := newHarness(t)
	for i := range maxExchanges + 6 {
		h.dhcp(time.Duration(i)*time.Millisecond, macA, "", client(observation.DHCPDiscover, uint32(i+1), macA))
	}
	got := h.query(`SELECT outcome, count(*) FROM dhcp_exchanges GROUP BY outcome ORDER BY outcome`, func(r *sql.Rows) string {
		var o string
		var n int
		_ = r.Scan(&o, &n)
		return fmt.Sprintf("%s=%d", o, n)
	})
	expect(t, "outcomes", got, []string{fmt.Sprintf("discover=%d", maxExchanges), "incomplete=6"})
	if n := len(h.c.st.exchanges); n != maxExchanges {
		t.Errorf("%d exchanges open", n)
	}
	// A message without a client hardware address, or of a type without a
	// part in a lease, is kept as its observation only.
	h.dhcp(time.Second, macA, "", observation.DHCPMessage{Op: 1, Type: observation.DHCPDiscover, XID: 9})
	h.dhcp(time.Second, macA, "", observation.DHCPMessage{Op: 1, Type: "", XID: 9, CHAddr: macA})
	if n := len(h.exchanges()); n != maxExchanges+6 {
		t.Errorf("%d exchange rows", n)
	}
}
