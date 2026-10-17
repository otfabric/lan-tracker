package correlate

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"sort"
	"time"

	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/identify"
	"github.com/otfabric/lan-tracker/internal/observation"
)

func (c *Correlator) hostByID(id string) *host {
	for _, h := range c.st.order {
		if h.id == id {
			return h
		}
	}
	return nil
}

// observe applies docs/DATA_MODEL.md §5 to one observation.
func (c *Correlator) observe(ctx context.Context, o observation.Observation) {
	n := c.st.contexts[o.Interface]
	if n == nil || o.Time.IsZero() {
		c.ignored.Add(1)
		return
	}
	c.advance(ctx, o.Time)

	mac := o.MAC
	if mac != nil && !identify.UnicastMAC(mac) {
		mac = nil
	}
	ip := o.IP.Unmap()
	if ip.IsValid() && !ip.IsGlobalUnicast() && !ip.IsLinkLocalUnicast() {
		ip = netip.Addr{}
	}
	if mac == nil && !ip.IsValid() && len(o.Records) == 0 {
		c.ignored.Add(1)
		return
	}
	if o.Time.After(n.lastSeen) {
		n.lastSeen, n.dirty = o.Time, true
	}
	c.count(o.Source, c.bySource)
	c.st.nextObservationID++
	obsID := c.st.nextObservationID
	ev := events.EvidenceFrom(o)
	kind := o.Evidence()

	// §5.1: bind the observation to a host, by the kind of evidence it is
	// (§2). A DHCP server's lease assertion is about the client in the
	// message, not the sender: it goes to the lease history (§5.6) and binds
	// nothing. A MAC-less observation describes
	// the one MAC that holds its IP, if exactly one does and its binding was
	// confirmed within identity.address_expiry; only an answer makes that
	// host present and confirms the binding.
	var h, holder *host
	discovered := false
	switch {
	case mac != nil:
		h, discovered = c.bindMAC(ctx, n, mac, o, obsID, ev, kind)
	case ip.IsValid():
		if hh := c.st.soleHolder(ipKey{n.id, ip}, o.Time, c.cfg.Load().Identity.AddressExpiry.D()); hh != nil {
			h, holder = hh, hh
			if kind.Present() {
				c.seen(ctx, h, o, ev, kind)
			}
		}
	}
	hostID := ""
	if h != nil {
		hostID = h.id
	} else {
		c.unbound.Add(1)
		c.count(o.Source, c.unboundBySource)
	}
	c.dbObservation(ctx, obsID, n.id, o, hostID)

	if h != nil && ip.IsValid() && kind != observation.EvidenceNone {
		c.attribute(ctx, h, ip, o, obsID, ev, discovered, kind != observation.EvidenceCached)
	}
	if o.Service != nil && o.Source == observation.TCPConnect && mac == nil && ip.IsValid() {
		c.endpointResult(ctx, n, ip, holder, o, obsID, ev)
	}
	sender := h
	if mac == nil {
		sender = nil // a NAME belongs only to the device that sent it
	}
	c.names(ctx, n, sender, o, obsID, ev)
	if m := o.DHCP; m != nil {
		if o.Source == observation.PassiveDHCPServer && h != nil && mac != nil { // the sender's own MAC, not an IP holder's
			c.dhcpServerReply(ctx, h, m, o, obsID, ev)
		}
		c.dhcpMessage(ctx, n, m, o, obsID, ev)
	}
}

// bindMAC returns the host for (context, MAC), creating it if needed. A
// known host is refreshed only by presence evidence: a cache entry (a
// STALE neighbour) records a MAC never seen before, but never makes a
// known one present again.
func (c *Correlator) bindMAC(ctx context.Context, n *netContext, mac net.HardwareAddr, o observation.Observation, obsID int64, ev events.Evidence, kind observation.Evidence) (*host, bool) {
	key := mac.String()
	if h := c.st.hosts[n.id][key]; h != nil {
		if kind.Present() {
			c.seen(ctx, h, o, ev, kind)
		}
		return h, false
	}
	h := newHost()
	h.id, h.ctx, h.mac, h.firstSeen, h.lastSeen, h.evidence = c.newID(), n, append(net.HardwareAddr(nil), mac...), o.Time, o.Time, ev
	h.mark(o, kind)
	if c.vendors != nil {
		h.vendor, _ = c.vendors.Lookup(mac)
	}
	h.presence = c.presenceAt(o.Time, c.refTime(o.Time))
	c.st.addHost(h)
	c.dbInsertHost(ctx, h, identify.LocallyAdministered(mac))

	e := hostEvent(events.HostDiscovered, o.Time, h, string(o.Source), ev)
	e.New, e.ObservationID = key, obsID
	c.emit(ctx, e)

	// The same MAC known on another interface is another host (§1). Seeing
	// it on a second interface does not establish that it moved (a bridge,
	// a mirror port and a duplicated MAC look the same), so the event says
	// only that it is also observed here.
	var other *host
	for _, x := range c.st.hostsByMAC[key] {
		if x != h && (other == nil || x.lastSeen.After(other.lastSeen)) {
			other = x
		}
	}
	if other != nil {
		e := hostEvent(events.MACAlsoObserved, o.Time, h, string(o.Source), ev)
		e.Old, e.New, e.RelatedHostID, e.RelatedMAC, e.ObservationID = other.ctx.iface, n.iface, other.id, other.mac.String(), obsID
		c.emit(ctx, e)
	}
	return h, true
}

// frameSources are address evidence that is also a frame from the MAC.
var frameSources = []observation.Source{observation.PassiveARP, observation.ARPScan, observation.PassiveNDP, observation.NDPProbe}

// mark moves the time of o's kind of evidence on h; it reports whether
// one moved.
func (h *host) mark(o observation.Observation, kind observation.Evidence) bool {
	later := func(t *time.Time) bool {
		if o.Time.After(*t) {
			*t = o.Time
			return true
		}
		return false
	}
	moved := false
	switch kind {
	case observation.EvidenceFrame:
		moved = later(&h.lastFrame)
	case observation.EvidenceAddress:
		moved = later(&h.lastAddress)
		if slices.Contains(frameSources, o.Source) {
			moved = later(&h.lastFrame) || moved
		}
	case observation.EvidenceAnswer:
		moved = later(&h.lastAnswer)
	}
	return moved
}

// seen records presence evidence of h.
func (c *Correlator) seen(ctx context.Context, h *host, o observation.Observation, ev events.Evidence, kind observation.Evidence) {
	moved := h.mark(o, kind)
	if o.Time.After(h.lastSeen) {
		h.lastSeen, h.evidence = o.Time, ev
	}
	p := c.presenceAt(h.lastSeen, c.refTime(o.Time))
	if h.presence == Missing && p != Missing {
		e := hostEvent(events.HostReappeared, o.Time, h, string(o.Source), ev)
		e.Old, e.New = string(Missing), string(p)
		c.emit(ctx, e)
	}
	if moved || p != h.presence {
		h.presence = p
		c.dbHostSeen(ctx, h)
	}
}

// onLink implements §5.2.
func onLink(n *netContext, ip netip.Addr, src observation.Source) bool {
	for p := range n.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	if ip.Is4() {
		switch src {
		case observation.PassiveARP, observation.ARPScan, observation.PassiveDHCP, observation.KernelNeighbor:
			return true
		}
		return false
	}
	if ip.IsLinkLocalUnicast() {
		return true
	}
	switch src {
	case observation.PassiveNDP, observation.NDPProbe, observation.KernelNeighbor:
		return true
	}
	return false
}

// notHostAddress reports the network and broadcast addresses of the
// context's IPv4 prefixes.
func notHostAddress(n *netContext, ip netip.Addr) bool {
	if !ip.Is4() {
		return false
	}
	for p := range n.prefixes {
		if !p.Addr().Is4() || p.Bits() >= 31 || !p.Contains(ip) {
			continue
		}
		if ip == p.Masked().Addr() || ip == lastAddr(p) {
			return true
		}
	}
	return false
}

func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	v |= host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// attribute applies the address binding lifecycle (§5.3) for ip on h. A
// cache entry (refresh false) may open a binding the host never had but
// never confirms one it has.
func (c *Correlator) attribute(ctx context.Context, h *host, ip netip.Addr, o observation.Observation, obsID int64, ev events.Evidence, discovered, refresh bool) {
	n := h.ctx
	if !onLink(n, ip, o.Source) || notHostAddress(n, ip) {
		return
	}
	k := ipKey{n.id, ip}
	// A late observation (older than the last change of the host or the IP)
	// only refreshes open bindings; it never changes them.
	late := o.Time.Before(h.lastChange) || o.Time.Before(c.st.ipLastChange[k])
	reply := isARPReply(o)
	if reply && c.proxyClaim(ctx, h, ip, o, obsID, ev, late) {
		return
	}
	if b := h.bindings[ip]; b != nil {
		if !refresh {
			return
		}
		if o.Time.After(b.lastSeen) {
			b.lastSeen = o.Time
		}
		b.arpOnly = b.arpOnly && reply
		c.dbBindingSeen(ctx, b, o.Source, o.Time)
		return
	}
	if late {
		return
	}
	cause := string(o.Source)

	// Takeover from hosts that are no longer live; conflict with live ones.
	var rivals []*host
	var wasConflict *host // a closed holder that was in conflict
	for _, g := range append([]*binding(nil), c.st.holders[k]...) {
		if c.presenceAt(g.host.lastSeen, o.Time).live() {
			rivals = append(rivals, g.host)
			continue
		}
		if g.conflict {
			wasConflict = g.host
		}
		c.close(g, o.Time)
		e := hostEvent(events.IPRemoved, o.Time, g.host, cause, ev)
		e.Old, e.RelatedHostID, e.RelatedMAC, e.ObservationID = ip.String(), h.id, h.mac.String(), obsID
		c.emit(ctx, e)
	}

	// A MAC's addresses are independent (§5.3): a new address never ends
	// another. Equipment with a production and a maintenance address, or
	// one moving to a new address while the old one is merely quiet, keeps
	// both until each ages out on its own evidence.
	c.st.nextAddressID++
	nb := &binding{id: c.st.nextAddressID, host: h, ip: ip, conflict: len(rivals) > 0, firstSeen: o.Time, lastSeen: o.Time, arpOnly: reply}
	c.st.addBinding(nb)
	c.dbOpenBinding(ctx, nb, o.Source)
	c.changed(h, k, o.Time)

	if !discovered {
		e := hostEvent(events.IPAdded, o.Time, h, cause, ev)
		e.New, e.ObservationID = ip.String(), obsID
		c.emit(ctx, e)
	}

	if len(rivals) == 0 && wasConflict != nil {
		// The takeover ended a conflict: one MAC holds the address again.
		e := hostEvent(events.DuplicateIPResolved, o.Time, h, cause, ev)
		e.New, e.RelatedHostID, e.RelatedMAC, e.ObservationID = ip.String(), wasConflict.id, wasConflict.mac.String(), obsID
		c.emit(ctx, e)
	}
	if len(rivals) > 0 {
		sort.Slice(rivals, func(i, j int) bool { return rivals[i].lastSeen.After(rivals[j].lastSeen) })
		for _, g := range c.st.holders[k] {
			if g != nb && !g.conflict {
				g.conflict = true
				c.dbConflict(ctx, g)
			}
		}
		e := hostEvent(events.DuplicateIPDetected, o.Time, h, cause, ev)
		e.New, e.RelatedHostID, e.RelatedMAC, e.ObservationID = ip.String(), rivals[0].id, rivals[0].mac.String(), obsID
		c.emit(ctx, e)
	}
}

// close ends binding b at endedAt (queued write, no event).
func (c *Correlator) close(b *binding, endedAt time.Time) {
	c.st.removeBinding(b)
	c.dbCloseBinding(context.Background(), b, endedAt)
	c.changed(b.host, ipKey{b.host.ctx.id, b.ip}, endedAt)
}

func (c *Correlator) changed(h *host, k ipKey, t time.Time) {
	if t.After(h.lastChange) {
		h.lastChange = t
	}
	c.st.touchIP(k, t)
}

// resolve clears a conflict once a single binding for the IP remains.
func (c *Correlator) resolve(ctx context.Context, k ipKey, t time.Time, cause string, obsID int64, ev events.Evidence, leaving *host) {
	hs := c.st.holders[k]
	if len(hs) != 1 || !hs[0].conflict {
		return
	}
	b := hs[0]
	b.conflict = false
	c.dbConflict(ctx, b)
	e := hostEvent(events.DuplicateIPResolved, t, b.host, cause, ev)
	e.New, e.RelatedHostID, e.RelatedMAC, e.ObservationID = k.ip.String(), leaving.id, leaving.mac.String(), obsID
	c.emit(ctx, e)
}

// advance evaluates presence (§6) and binding expiry (§5.3) up to now.
// Transitions are stamped with the moment the threshold was crossed.
func (c *Correlator) advance(ctx context.Context, now time.Time) {
	if !now.After(c.advancedTo) {
		return
	}
	c.advancedTo = now
	c.expireExchanges(ctx, now)
	cfg := c.cfg.Load()
	stale, expiry := cfg.Presence.Stale.D(), cfg.Identity.AddressExpiry.D()

	for _, h := range c.st.order {
		// Expiry: unconfirmed for address_expiry while the host was seen since.
		for _, b := range sortedBindings(h) {
			if now.Sub(b.lastSeen) < expiry || !h.lastSeen.After(b.lastSeen) {
				continue
			}
			ended := b.lastSeen.Add(expiry)
			c.close(b, ended)
			ev := h.evidence
			ev.Reason = "address not confirmed for " + cfg.Identity.AddressExpiry.String()
			e := hostEvent(events.IPRemoved, ended, h, causeExpiry, ev)
			e.Old = b.ip.String()
			c.emit(ctx, e)
			c.resolve(ctx, ipKey{h.ctx.id, b.ip}, ended, causeExpiry, 0, ev, h)
		}

		p := c.presenceAt(h.lastSeen, now)
		if p == h.presence {
			continue
		}
		h.presence = p
		c.dbHostSeen(ctx, h)
		if p == Missing {
			ev := h.evidence
			ev.Reason = "no observation for " + cfg.Presence.Stale.String()
			// The state just before MISSING is always STALE (thresholds are
			// ordered), however coarsely presence was evaluated.
			e := hostEvent(events.HostDisappeared, h.lastSeen.Add(stale), h, causePresence, ev)
			e.Old, e.New = string(Stale), string(Missing)
			c.emit(ctx, e)
		}
	}
}

func sortedBindings(h *host) []*binding {
	out := make([]*binding, 0, len(h.bindings))
	for _, b := range h.bindings {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ip.Less(out[j].ip) })
	return out
}

// link applies an interface state: INTERFACE_UP/DOWN on transitions and
// SUBNET_CHANGED for every prefix opened or closed.
func (c *Correlator) link(ctx context.Context, ls observation.LinkState) {
	n := c.st.contexts[ls.Interface]
	if n == nil {
		return
	}
	c.advance(ctx, ls.Time)
	up := ls.Present && ls.Up
	if n.up != nil && *n.up != up {
		t, state := events.InterfaceDown, "down"
		if up {
			t, state = events.InterfaceUp, "up"
		}
		c.emit(ctx, events.Event{TS: ls.Time, Type: t, ContextID: n.id, Interface: n.iface, New: state, Cause: causeIface,
			Evidence: events.Evidence{TS: ls.Time, Interface: n.iface}})
	}
	n.up = &up

	want := map[netip.Prefix]bool{}
	for _, p := range ls.Prefixes {
		want[p.Masked()] = true
	}
	var closed, opened []netip.Prefix
	for p := range n.prefixes {
		if !want[p] {
			closed = append(closed, p)
		}
	}
	for p := range want {
		if n.prefixes[p] == nil {
			opened = append(opened, p)
		}
	}
	sortPrefixes(closed)
	sortPrefixes(opened)
	for _, p := range closed {
		c.dbClosePrefix(ctx, n.prefixes[p].id, ls.Time)
		delete(n.prefixes, p)
		c.subnetEvent(ctx, n, ls.Time, describe(p), "")
	}
	for _, p := range opened {
		c.st.nextPrefixID++
		n.prefixes[p] = &prefixRow{id: c.st.nextPrefixID, firstSeen: ls.Time}
		c.dbOpenPrefix(ctx, n, c.st.nextPrefixID, p, ls.Time)
		c.subnetEvent(ctx, n, ls.Time, "", describe(p))
	}
}

func (c *Correlator) subnetEvent(ctx context.Context, n *netContext, t time.Time, old, new string) {
	c.emit(ctx, events.Event{TS: t, Type: events.SubnetChanged, ContextID: n.id, Interface: n.iface, Old: old, New: new,
		Cause: causeIface, Evidence: events.Evidence{TS: t, Interface: n.iface}})
}

func sortPrefixes(ps []netip.Prefix) {
	sort.Slice(ps, func(i, j int) bool {
		if c := ps[i].Addr().Compare(ps[j].Addr()); c != 0 {
			return c < 0
		}
		return ps[i].Bits() < ps[j].Bits()
	})
}
