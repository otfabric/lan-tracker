package correlate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/observation"
)

// DHCP server allowlist verdicts (docs/DATA_MODEL.md §5.6).
const (
	dhcpAllowed    = "allowed"
	dhcpUnexpected = "unexpected"
	dhcpUnchecked  = "unchecked" // no allowlist on the interface
)

// Exchange limits: a transaction is closed this long after its last
// message, and a context keeps at most maxExchanges open (the oldest is
// closed to make room).
const (
	exchangeIdle  = time.Minute
	maxExchanges  = 1024
	maxExchangeMs = 16 // distinct messages kept per exchange
)

// dhcpServer is one dhcp_servers row: a server identity seen through one
// relay (or none) from one sender MAC.
type dhcpServer struct {
	id        int64
	key       dhcpKey
	ip        string
	hostID    string
	status    string
	config    map[string]string
	firstSeen time.Time
	lastSeen  time.Time
}

type dhcpKey struct {
	ctx      int64
	serverID string // option 54; "" when unknown
	relay    string // giaddr; "" when not relayed
	mac      string // the sender's
}

// identity names a server in events: its identifier ("unknown" without
// one), and the relay it was seen through.
func (k dhcpKey) identity() string {
	s := k.serverID
	if s == "" {
		s = "unknown"
	}
	if k.relay != "" {
		s += " via " + k.relay
	}
	return s
}

// dhcpStatus is the allowlist verdict for a server identifier on iface.
func dhcpStatus(cfg *config.Config, iface, serverID string) string {
	for _, ic := range cfg.Interfaces {
		if ic.Name != iface {
			continue
		}
		if !ic.DHCP.Checked() {
			return dhcpUnchecked
		}
		id, _ := netip.ParseAddr(serverID)
		if ic.DHCP.Allowed(id) {
			return dhcpAllowed
		}
		return dhcpUnexpected
	}
	return dhcpUnchecked
}

// dhcpServerReply applies §5.6 to the sender of a DHCP server reply: it
// records the server identity, relay and sender, and emits
// DHCP_SERVER_DISCOVERED for an identity new on the interface,
// DHCP_SERVER_MAC_CHANGED when a known identity replies from another
// sender MAC, and DHCP_SERVER_UNEXPECTED when the allowlist does not hold
// it. The configuration a server advertises is per client (a server may
// answer clients with different options): it is compared in each client's
// lease history, never across clients; the row only shows the last one
// seen.
func (c *Correlator) dhcpServerReply(ctx context.Context, h *host, m *observation.DHCPMessage, o observation.Observation, obsID int64, ev events.Evidence) {
	if m.Type != observation.DHCPOffer && m.Type != observation.DHCPAck && m.Type != observation.DHCPNak {
		return
	}
	k := dhcpKey{ctx: h.ctx.id, mac: h.mac.String()}
	if m.ServerID.IsValid() {
		k.serverID = m.ServerID.String()
	}
	if m.Relayed() {
		k.relay = m.GIAddr.String()
	}
	status := dhcpStatus(c.cfg.Load(), h.ctx.iface, k.serverID)
	// An INFORM's ACK assigns nothing: the row keeps what servers offer
	// and assign.
	var advertised map[string]string
	if m.Type == observation.DHCPOffer || m.Allocates() {
		advertised = map[string]string{}
		if len(m.Routers) > 0 {
			advertised["router"] = joinAddrs(m.Routers)
		}
		if len(m.DNS) > 0 {
			advertised["dns"] = joinAddrs(m.DNS)
		}
		if m.SubnetMask.IsValid() {
			advertised["subnet_mask"] = m.SubnetMask.String()
		}
	}
	ip := ""
	if o.IP.IsValid() {
		ip = o.IP.Unmap().String()
	}
	event := func(t events.Type, old, nw string) events.Event {
		e := hostEvent(t, o.Time, h, string(o.Source), ev)
		e.Old, e.New, e.ObservationID = old, nw, obsID
		return e
	}

	s := c.st.dhcpServers[k]
	if s == nil {
		s = &dhcpServer{key: k, ip: ip, hostID: h.id, status: status, config: map[string]string{}, firstSeen: o.Time, lastSeen: o.Time}
		for key, v := range advertised {
			s.config[key] = v
		}
		known, previous := c.st.dhcpPrevious(k)
		c.st.nextDHCPServerID++
		s.id = c.st.nextDHCPServerID
		c.st.dhcpServers[k] = s
		c.dbInsertDHCPServer(ctx, s)
		switch {
		case !known:
			c.emit(ctx, event(events.DHCPServerDiscovered, "", k.identity()))
		case previous != nil:
			e := event(events.DHCPServerMACChanged, previous.key.mac, k.mac)
			e.RelatedHostID, e.RelatedMAC = previous.hostID, previous.key.mac
			c.emit(ctx, e)
		}
		if status == dhcpUnexpected {
			c.emit(ctx, event(events.DHCPServerUnexpected, "", k.identity()))
		}
		return
	}
	if o.Time.Before(s.lastSeen) {
		return // older evidence: the row already says more
	}
	s.lastSeen, s.hostID = o.Time, h.id
	if ip != "" {
		s.ip = ip
	}
	if status != s.status {
		old := s.status
		s.status = status
		if status == dhcpUnexpected {
			c.emit(ctx, event(events.DHCPServerUnexpected, old, k.identity()))
		}
	}
	for key, v := range advertised {
		s.config[key] = v // the last configuration seen; an absent option is no change
	}
	c.dbUpdateDHCPServer(ctx, s)
}

func joinAddrs(as []netip.Addr) string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.String()
	}
	return strings.Join(out, ",")
}

// dhcpPrevious reports whether k's server identity is already known on the
// interface and, if it is known through the same relay from other sender
// MACs, the one seen last. A reply without an identifier is told apart by
// its sender alone, so it is never a MAC change.
func (s *state) dhcpPrevious(k dhcpKey) (known bool, previous *dhcpServer) {
	if k.serverID == "" {
		return false, nil
	}
	for x, row := range s.dhcpServers {
		if x.ctx != k.ctx || x.serverID != k.serverID {
			continue
		}
		known = true
		if x.relay == k.relay && x.mac != k.mac &&
			(previous == nil || row.lastSeen.After(previous.lastSeen) || row.lastSeen.Equal(previous.lastSeen) && row.id < previous.id) {
			previous = row
		}
	}
	return known, previous
}

// exchKey is one DHCP transaction. The xid alone is not unique: clients
// pick it, so the client's hardware address is part of the key. A reply
// without the client identifier its request had is the same exchange.
type exchKey struct {
	ctx    int64
	xid    uint32
	chaddr string
}

type exchMsg struct {
	Type   string `json:"type"`
	Server string `json:"server_id,omitempty"`
	YIAddr string `json:"yiaddr,omitempty"`
	Sender string `json:"sender_mac,omitempty"`
	Count  int    `json:"count"`
}

type exchange struct {
	id                  int64
	key                 exchKey
	clientID            string
	hostID              string
	firstSeen, lastSeen time.Time
	msgs                []exchMsg
	requested           string
	selected            string
	relayed             bool
	leaseID             int64
	config              map[string]any // an INFORM's ACK
	nakSeen             bool
}

// outcome and visibility of an exchange from its messages: the last
// decisive message, and whether both the client's side and the server's
// were seen where the exchange needs both.
func (x *exchange) outcome(closed bool) (string, string) {
	has := func(t string) bool {
		return slices.ContainsFunc(x.msgs, func(m exchMsg) bool { return m.Type == t })
	}
	client := has(observation.DHCPDiscover) || has(observation.DHCPRequest) || has(observation.DHCPInform)
	server := has(observation.DHCPOffer) || has(observation.DHCPAck) || has(observation.DHCPNak)
	out := ""
	for _, m := range x.msgs {
		switch m.Type {
		case observation.DHCPAck:
			out = "ack"
			if m.YIAddr == "" {
				out = "inform_ack"
			}
		case observation.DHCPNak, observation.DHCPDecline, observation.DHCPRelease:
			out = m.Type
		case observation.DHCPOffer:
			if out == "" || out == "discover" {
				out = "offered"
			}
		case observation.DHCPRequest, observation.DHCPInform, observation.DHCPDiscover:
			if out == "" || out == "discover" {
				out = m.Type
			}
		}
	}
	vis := "partial"
	switch {
	case out == "release" || out == "decline":
		vis = "complete" // the client expects no answer
	case client && server:
		vis = "complete"
	}
	if closed && (out == "discover" || out == "request" || out == "inform") {
		out = "incomplete" // the answer, if there was one, was not seen
	}
	return out, vis
}

// dhcpLease is a client's open lease (dhcp_leases).
type dhcpLease struct {
	id       int64
	ip       string
	serverID string
	config   map[string]any
	lastAck  time.Time
	acks     int
}

type leaseKey struct {
	ctx    int64
	chaddr string
}

// dhcpMessage applies §5.6 to one DHCP message seen on n: it adds it to
// its exchange and, for an ACK that assigns or renews an address, to the
// client's lease history. The client is the message's chaddr; its host,
// if it has one, is only referenced: a lease is the server's statement,
// so it neither creates the host nor makes it present, and a relayed
// message may describe a client on another link.
func (c *Correlator) dhcpMessage(ctx context.Context, n *netContext, m *observation.DHCPMessage, o observation.Observation, obsID int64, ev events.Evidence) {
	switch m.Type {
	case observation.DHCPDiscover, observation.DHCPOffer, observation.DHCPRequest, observation.DHCPDecline, observation.DHCPAck,
		observation.DHCPNak, observation.DHCPRelease, observation.DHCPInform:
	default:
		return // BOOTP, or a type without a part in a lease: the observation only
	}
	if m.CHAddr == "" {
		return // not an Ethernet client: kept as the observation only
	}
	k := exchKey{ctx: n.id, xid: m.XID, chaddr: m.CHAddr}
	x := c.st.exchanges[k]
	if x != nil && o.Time.Sub(x.lastSeen) > exchangeIdle {
		c.closeExchange(ctx, x)
		x = nil
	}
	insert := x == nil
	if insert {
		c.st.nextExchangeID++
		x = &exchange{id: c.st.nextExchangeID, key: k, firstSeen: o.Time, lastSeen: o.Time}
		c.st.exchanges[k] = x
		c.limitExchanges(ctx, n.id)
	}
	if o.Time.After(x.lastSeen) {
		x.lastSeen = o.Time
	}
	if m.ClientID != "" {
		x.clientID = m.ClientID
	}
	if hc := c.st.hosts[n.id][m.CHAddr]; hc != nil {
		x.hostID = hc.id
	}
	x.relayed = x.relayed || m.Relayed()
	msg := exchMsg{Type: m.Type, Count: 1}
	if m.ServerID.IsValid() {
		msg.Server = m.ServerID.String()
	}
	if m.Op == 2 && len(o.MAC) > 0 {
		msg.Sender = o.MAC.String()
	}
	if m.Allocates() || m.Type == observation.DHCPOffer {
		msg.YIAddr = m.YIAddr.String()
	}
	if last := len(x.msgs) - 1; last >= 0 && x.msgs[last].Type == msg.Type && x.msgs[last].Server == msg.Server && x.msgs[last].YIAddr == msg.YIAddr {
		x.msgs[last].Count++ // a retransmission
	} else if len(x.msgs) < maxExchangeMs {
		x.msgs = append(x.msgs, msg)
	}
	switch m.Type {
	case observation.DHCPRequest:
		if m.RequestedIP.IsValid() {
			x.requested = m.RequestedIP.String()
		} else if m.CIAddr.IsValid() && !m.CIAddr.IsUnspecified() {
			x.requested = m.CIAddr.String() // renewing
		}
		if m.ServerID.IsValid() {
			x.selected = m.ServerID.String()
		}
	case observation.DHCPAck:
		if !m.Allocates() {
			x.config = leaseConfig(m)
		}
	}
	host := c.st.hosts[n.id][m.CHAddr]
	event := func(t events.Type, old, nw string) {
		e := events.Event{TS: o.Time, Type: t, ContextID: n.id, Interface: n.iface, MAC: m.CHAddr, Old: old, New: nw,
			Cause: string(o.Source), ObservationID: obsID, Evidence: ev}
		if host != nil {
			e.HostID = host.id
		}
		c.emit(ctx, e)
	}
	lk := leaseKey{ctx: n.id, chaddr: m.CHAddr}
	cur := c.st.leases[lk]
	switch {
	case m.Allocates():
		c.leaseAck(ctx, n, x, m, o, host, event)
	case m.Type == observation.DHCPNak && !x.nakSeen:
		x.nakSeen = true
		event(events.DHCPLeaseNAK, x.requested, nakText(m))
	case m.Type == observation.DHCPDecline:
		declined := ""
		if m.RequestedIP.IsValid() {
			declined = m.RequestedIP.String() // option 50: the address declined
		}
		event(events.DHCPLeaseDeclined, declined, "")
		if cur != nil && declined != "" && cur.ip == declined {
			c.endLease(ctx, lk, o.Time, "declined")
		}
	case m.Type == observation.DHCPRelease:
		released := m.CIAddr.String()
		event(events.DHCPLeaseReleased, released, "")
		if cur != nil && cur.ip == released {
			c.endLease(ctx, lk, o.Time, "released")
		}
	}
	c.dbExchange(ctx, x, insert, false)
}

func nakText(m *observation.DHCPMessage) string {
	if m.ServerID.IsValid() {
		return "nak from " + m.ServerID.String()
	}
	return "nak"
}

// leaseConfig is the ACK snapshot: the configuration options it carried,
// and the gateway they imply. An option the ACK left out is not in it:
// unknown, never empty.
func leaseConfig(m *observation.DHCPMessage) map[string]any {
	cfg := map[string]any{}
	if m.SubnetMask.IsValid() {
		cfg["subnet_mask"] = m.SubnetMask.String()
		if bits, ok := maskBits(m.SubnetMask); ok {
			cfg["prefix_length"] = bits
		}
	}
	if len(m.Routers) > 0 {
		cfg["routers"] = joinAddrs(m.Routers)
	}
	if len(m.DNS) > 0 {
		cfg["dns"] = joinAddrs(m.DNS)
	}
	if m.Domain != "" {
		cfg["domain"] = m.Domain
	}
	if len(m.Search) > 0 {
		cfg["search"] = strings.Join(m.Search, ",")
	}
	if m.Hostname != "" {
		cfg["hostname"] = m.Hostname
	}
	if m.FQDN != nil {
		cfg["fqdn"] = m.FQDN.Name
		cfg["fqdn_flags"] = int(m.FQDN.Flags)
	}
	routes := func(rs []observation.DHCPRoute) string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.Destination.String() + " via " + r.Router.String()
		}
		return strings.Join(out, ",")
	}
	if m.Has(121) {
		cfg["classless_routes"] = routes(m.Routes)
	}
	if m.Has(249) {
		cfg["ms_classless_routes"] = routes(m.MSRoutes)
	}
	if gw, from := m.Gateway(); from != "" {
		cfg["gateway_source"] = from
		if gw.IsValid() {
			cfg["gateway"] = gw.String()
		}
	}
	if m.Has(82) && m.RelayAgent != nil {
		cfg["relay_circuit"], cfg["relay_remote"] = m.RelayAgent.CircuitID, m.RelayAgent.RemoteID
	}
	if len(m.Malformed) > 0 {
		cfg["malformed_options"] = m.Malformed
	}
	return cfg
}

func maskBits(mask netip.Addr) (int, bool) {
	ones, bits := net.IPMask(mask.AsSlice()).Size()
	return ones, bits == 32
}

// sameConfig compares two snapshots on the options both carried: one a
// reply left out is unknown, not a change.
func sameConfig(a, b map[string]any) bool {
	for k, va := range a {
		if vb, ok := b[k]; ok && fmt.Sprint(va) != fmt.Sprint(vb) {
			return false
		}
	}
	return true
}

// configChanges lists the options both snapshots carried that differ.
func configChanges(a, b map[string]any) (string, string) {
	keys := make([]string, 0, len(b))
	for k := range b {
		if _, ok := a[k]; ok && fmt.Sprint(a[k]) != fmt.Sprint(b[k]) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var olds, news []string
	for _, k := range keys {
		olds, news = append(olds, fmt.Sprintf("%s=%v", k, a[k])), append(news, fmt.Sprintf("%s=%v", k, b[k]))
	}
	return strings.Join(olds, " "), strings.Join(news, " ")
}

// leaseAck records an ACK that assigns or renews an address. The same
// address, server and configuration renews the client's open lease; a
// different address is a new lease (DHCP_LEASE_ASSIGNED), a different
// configuration for the same client a new snapshot
// (DHCP_CLIENT_CONFIG_CHANGED). Repeated identical renewals are rows'
// counts, not events.
func (c *Correlator) leaseAck(ctx context.Context, n *netContext, x *exchange, m *observation.DHCPMessage, o observation.Observation,
	host *host, event func(events.Type, string, string)) {
	lk := leaseKey{ctx: n.id, chaddr: m.CHAddr}
	cur := c.st.leases[lk]
	ip, sid := m.YIAddr.String(), ""
	if m.ServerID.IsValid() {
		sid = m.ServerID.String()
	}
	snap := leaseConfig(m)
	lease, expires, t1, t2, derived := leaseTimes(m, o.Time)
	if cur != nil && o.Time.Before(cur.lastAck) {
		return // late: the history already has a newer ACK
	}
	if cur != nil && cur.ip == ip && cur.serverID == sid && sameConfig(cur.config, snap) {
		cur.lastAck = o.Time
		cur.acks++
		x.leaseID = cur.id
		c.exec(ctx, "renew lease", `UPDATE dhcp_leases SET last_ack = ?, acks = acks + 1, expires_at = ?, lease_seconds = coalesce(?, lease_seconds),
			t1_seconds = coalesce(?, t1_seconds), t2_seconds = coalesce(?, t2_seconds) WHERE id = ?`,
			ms(o.Time), expires, lease, t1, t2, cur.id)
		return
	}
	if cur != nil {
		c.endLease(ctx, lk, o.Time, "superseded")
		switch {
		case cur.ip != ip:
			event(events.DHCPLeaseAssigned, cur.ip, ip)
		default:
			old, nw := configChanges(cur.config, snap)
			if cur.serverID != sid {
				old, nw = strings.TrimSpace("server_id="+cur.serverID+" "+old), strings.TrimSpace("server_id="+sid+" "+nw)
			}
			event(events.DHCPClientConfigChanged, old, nw)
		}
	} else {
		event(events.DHCPLeaseAssigned, "", ip)
	}
	c.st.nextLeaseID++
	l := &dhcpLease{id: c.st.nextLeaseID, ip: ip, serverID: sid, config: snap, lastAck: o.Time, acks: 1}
	c.st.leases[lk] = l
	x.leaseID = l.id
	hostID := ""
	if host != nil {
		hostID = host.id
	}
	sender, senderIP, relay := "", "", ""
	if len(o.MAC) > 0 {
		sender = o.MAC.String()
	}
	if o.IP.IsValid() {
		senderIP = o.IP.String()
	}
	if m.Relayed() {
		relay = m.GIAddr.String()
	}
	var circuit, remote any
	if m.RelayAgent != nil {
		circuit, remote = nullString(m.RelayAgent.CircuitID), nullString(m.RelayAgent.RemoteID)
	}
	cfg, _ := json.Marshal(snap)
	partial := !slices.ContainsFunc(x.msgs, func(mm exchMsg) bool { return mm.Type == observation.DHCPRequest })
	c.exec(ctx, "insert lease", `INSERT INTO dhcp_leases (id, context_id, chaddr, client_id, host_id, ip, server_id, sender_mac, sender_ip,
		relay, relay_circuit, relay_remote, first_ack, last_ack, lease_seconds, expires_at, t1_seconds, t2_seconds, t_derived, config_json,
		partial, incomplete) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		l.id, n.id, m.CHAddr, nullString(firstNonEmpty(m.ClientID, x.clientID)), nullString(hostID), ip, sid, sender, senderIP, relay,
		circuit, remote, ms(o.Time), ms(o.Time), lease, expires, t1, t2, boolInt(derived), string(cfg), boolInt(partial),
		boolInt(m.Truncated || len(m.Malformed) > 0))
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// leaseTimes reads the lease time (-1 infinite, nil unknown), the expiry
// per this ACK, and T1/T2 as advertised or, without them, the RFC 2131
// defaults (half and seven eighths of the lease), marked derived.
func leaseTimes(m *observation.DHCPMessage, at time.Time) (lease, expires, t1, t2 any, derived bool) {
	if m.Lease == nil {
		return nil, nil, opt32(m.T1), opt32(m.T2), false
	}
	if *m.Lease == ^uint32(0) {
		return int64(-1), nil, opt32(m.T1), opt32(m.T2), false
	}
	l := int64(*m.Lease)
	lease, expires = l, ms(at)+1000*l
	t1, t2 = opt32(m.T1), opt32(m.T2)
	if m.T1 == nil && m.T2 == nil {
		t1, t2, derived = l/2, l*7/8, true
	}
	return lease, expires, t1, t2, derived
}

func opt32(v *uint32) any {
	if v == nil {
		return nil
	}
	return int64(*v)
}

// endLease ends a client's open lease at t.
func (c *Correlator) endLease(ctx context.Context, k leaseKey, t time.Time, reason string) {
	l := c.st.leases[k]
	if l == nil {
		return
	}
	delete(c.st.leases, k)
	c.exec(ctx, "end lease", `UPDATE dhcp_leases SET ended_at = max(first_ack, ?), end_reason = ? WHERE id = ?`, ms(t), reason, l.id)
}

// closeExchange writes an exchange's final outcome and forgets it.
func (c *Correlator) closeExchange(ctx context.Context, x *exchange) {
	delete(c.st.exchanges, x.key)
	c.dbExchange(ctx, x, false, true)
}

// limitExchanges closes the oldest open exchanges of a context beyond
// maxExchanges.
func (c *Correlator) limitExchanges(ctx context.Context, cid int64) {
	var open []*exchange
	for _, x := range c.st.exchanges {
		if x.key.ctx == cid {
			open = append(open, x)
		}
	}
	if len(open) <= maxExchanges {
		return
	}
	sort.Slice(open, func(i, j int) bool {
		return open[i].lastSeen.Before(open[j].lastSeen) || open[i].lastSeen.Equal(open[j].lastSeen) && open[i].id < open[j].id
	})
	for _, x := range open[:len(open)-maxExchanges] {
		c.closeExchange(ctx, x)
	}
}

// expireExchanges closes the exchanges idle since before now. It looks
// at most every exchangeIdle/4 of observation time.
func (c *Correlator) expireExchanges(ctx context.Context, now time.Time) {
	if now.Sub(c.st.exchangesSwept) < exchangeIdle/4 {
		return
	}
	c.st.exchangesSwept = now
	var idle []*exchange
	for _, x := range c.st.exchanges {
		if now.Sub(x.lastSeen) > exchangeIdle {
			idle = append(idle, x)
		}
	}
	sort.Slice(idle, func(i, j int) bool { return idle[i].id < idle[j].id })
	for _, x := range idle {
		c.closeExchange(ctx, x)
	}
}

func (c *Correlator) dbExchange(ctx context.Context, x *exchange, insert, closed bool) {
	out, vis := x.outcome(closed)
	var parts []string
	var offers []exchMsg
	for _, m := range x.msgs {
		p := m.Type
		if m.Server != "" || m.YIAddr != "" {
			p += "(" + strings.Trim(m.Server+" "+m.YIAddr, " ") + ")"
		}
		if m.Count > 1 {
			p += fmt.Sprintf("×%d", m.Count)
		}
		parts = append(parts, p)
		if m.Type == observation.DHCPOffer {
			offers = append(offers, m)
		}
	}
	if offers == nil {
		offers = []exchMsg{}
	}
	ob, _ := json.Marshal(offers)
	var cfg any
	if x.config != nil {
		b, _ := json.Marshal(x.config)
		cfg = string(b)
	}
	var lease any
	if x.leaseID != 0 {
		lease = x.leaseID
	}
	if insert {
		c.exec(ctx, "insert dhcp exchange", `INSERT INTO dhcp_exchanges (id, context_id, xid, chaddr, client_id, host_id, first_seen,
			last_seen, messages, requested_ip, offers_json, selected_server, outcome, visibility, relayed, config_json, lease_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			x.id, x.key.ctx, int64(x.key.xid), x.key.chaddr, nullString(x.clientID), nullString(x.hostID), ms(x.firstSeen), ms(x.lastSeen),
			strings.Join(parts, " "), nullString(x.requested), string(ob), nullString(x.selected), out, vis, boolInt(x.relayed), cfg, lease)
		return
	}
	c.exec(ctx, "update dhcp exchange", `UPDATE dhcp_exchanges SET client_id = ?, host_id = ?, last_seen = ?, messages = ?,
		requested_ip = ?, offers_json = ?, selected_server = ?, outcome = ?, visibility = ?, relayed = ?, config_json = ?, lease_id = ?
		WHERE id = ?`,
		nullString(x.clientID), nullString(x.hostID), ms(x.lastSeen), strings.Join(parts, " "), nullString(x.requested), string(ob),
		nullString(x.selected), out, vis, boolInt(x.relayed), cfg, lease, x.id)
}

func (c *Correlator) dbInsertDHCPServer(ctx context.Context, s *dhcpServer) {
	cfg, _ := json.Marshal(s.config)
	c.exec(ctx, "insert dhcp server", `INSERT INTO dhcp_servers
		(id, context_id, server_id, relay, mac, ip, host_id, status, config_json, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.id, s.key.ctx, s.key.serverID, s.key.relay, s.key.mac, s.ip, nullString(s.hostID), s.status, string(cfg),
		ms(s.firstSeen), ms(s.lastSeen))
}

func (c *Correlator) dbUpdateDHCPServer(ctx context.Context, s *dhcpServer) {
	cfg, _ := json.Marshal(s.config)
	c.exec(ctx, "update dhcp server", `UPDATE dhcp_servers SET ip = ?, host_id = ?, status = ?, config_json = ?, last_seen = ? WHERE id = ?`,
		s.ip, nullString(s.hostID), s.status, string(cfg), ms(s.lastSeen), s.id)
}

// loadDHCPServers reads dhcp_servers into the state.
func (s *state) loadDHCPServers(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, context_id, server_id, relay, mac, ip, coalesce(host_id, ''), status, config_json,
		first_seen, last_seen FROM dhcp_servers ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		d := &dhcpServer{}
		var cfg string
		var first, last int64
		if err := rows.Scan(&d.id, &d.key.ctx, &d.key.serverID, &d.key.relay, &d.key.mac, &d.ip, &d.hostID, &d.status, &cfg,
			&first, &last); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(cfg), &d.config); err != nil || d.config == nil {
			d.config = map[string]string{}
		}
		d.firstSeen, d.lastSeen = fromMS(first), fromMS(last)
		s.dhcpServers[d.key] = d
		s.nextDHCPServerID = max(s.nextDHCPServerID, d.id)
	}
	return rows.Err()
}

// loadLeases reads the clients' open leases.
func (s *state) loadLeases(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, context_id, chaddr, ip, server_id, config_json, last_ack, acks FROM dhcp_leases
		WHERE ended_at IS NULL ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		l := &dhcpLease{}
		var k leaseKey
		var cfg string
		var last int64
		if err := rows.Scan(&l.id, &k.ctx, &k.chaddr, &l.ip, &l.serverID, &cfg, &last, &l.acks); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(cfg), &l.config); err != nil || l.config == nil {
			l.config = map[string]any{}
		}
		l.lastAck = fromMS(last)
		s.leases[k] = l
	}
	return rows.Err()
}
