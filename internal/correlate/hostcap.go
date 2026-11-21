package correlate

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/store"
)

// The host cap (ADR 0013, docs/DATA_MODEL.md §5.9). A segment that shows
// many distinct source MACs (a misbehaving device, a MAC table overflow)
// would otherwise grow the correlator's current state without bound.
// identity.max_hosts_per_interface caps the hosts held per interface: a
// new MAC on a full interface evicts the host missing the longest, or,
// while no host is missing, its observation is dropped. The same cap
// bounds the open DHCP leases per interface, the oldest ending first.

// Name record end reason for the records of an evicted host.
const endHostEvicted = "host evicted"

// Lease end reason when the lease cap ends the oldest open lease.
const endLeaseEvicted = "evicted"

// capCounters count, per interface, what the cap did
// (lan_tracker_hosts_evicted_total, lan_tracker_observations_refused_total,
// lan_tracker_dhcp_leases_evicted_total).
type capCounters struct {
	evicted, refused, leasesEvicted atomic.Uint64
}

// CapCounts is what the host cap did on one interface since start.
type CapCounts struct {
	HostsEvicted        uint64 // hosts evicted to make room for a new MAC
	ObservationsRefused uint64 // observations dropped: a new MAC and no host missing
	LeasesEvicted       uint64 // open DHCP leases ended to make room for a new client
}

// CapCounts returns the host cap's counters per interface.
func (c *Correlator) CapCounts() map[string]CapCounts {
	out := make(map[string]CapCounts, len(c.st.contextsByID))
	for _, n := range c.st.contextsByID {
		out[n.iface] = CapCounts{n.caps.evicted.Load(), n.caps.refused.Load(), n.caps.leasesEvicted.Load()}
	}
	return out
}

func (c *Correlator) hostCap() int { return c.cfg.Load().Identity.MaxHostsPerInterface }

// admit makes room on n for a host of mac. While n is full, the host
// missing the longest is evicted; when none is missing the observation is
// refused: counted, and HOST_CAP_REACHED emitted once until a new host is
// admitted again on n. A MAC that was evicted from n is held again under
// its host_id (reinstate). Its row is looked up before anything is
// evicted, so a failed read evicts nothing: the observation is dropped and
// counted as unbound, rather than risk a second row for the MAC.
func (c *Correlator) admit(ctx context.Context, n *netContext, o observation.Observation, mac net.HardwareAddr) bool {
	row, back, err := c.evictedRow(ctx, n, mac)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Error("correlator: reading an evicted host failed", "interface", n.iface, "err", err)
		}
		c.count(o.Source, c.bySource)
		c.unbound.Add(1)
		c.count(o.Source, c.unboundBySource)
		return false
	}
	limit := c.hostCap()
	for len(c.st.hosts[n.id]) >= limit {
		h, ok := n.hostAges().peek()
		if !ok || h.presence != Missing {
			c.refuse(ctx, n, o, mac, limit)
			return false
		}
		c.evict(ctx, h, o, mac)
	}
	n.refusing = false
	if back {
		c.reinstate(ctx, n, mac, row)
	}
	return true
}

func (c *Correlator) refuse(ctx context.Context, n *netContext, o observation.Observation, mac net.HardwareAddr, limit int) {
	n.caps.refused.Add(1)
	if n.refusing {
		return
	}
	n.refusing = true
	ev := events.EvidenceFrom(o)
	ev.Reason = fmt.Sprintf("%d hosts held and none missing; new MACs are not tracked until one is", limit)
	c.emit(ctx, events.Event{TS: o.Time, Type: events.HostCapReached, ContextID: n.id, Interface: n.iface, MAC: mac.String(),
		New: strconv.Itoa(limit), Cause: string(o.Source), Evidence: ev})
}

// evict forgets h, the host missing the longest on its context, to make
// room for mac. Its open bindings close (IP_REMOVED) and its open name
// records end at the eviction; the hosts row and its history stay, marked
// evicted_at, so the MAC seen again is the same host (reinstate).
func (c *Correlator) evict(ctx context.Context, h *host, o observation.Observation, mac net.HardwareAddr) {
	t := c.refTime(o.Time)
	ev := h.evidence
	ev.Reason = fmt.Sprintf("missing since %s; interface at identity.max_hosts_per_interface (%d)",
		h.lastSeen.Add(c.timing.stale).UTC().Format("2006-01-02T15:04:05Z"), c.hostCap())
	for _, b := range sortedBindings(h) {
		c.close(b, t)
		e := hostEvent(events.IPRemoved, t, h, causeHostCap, ev)
		e.Old = b.ip.String()
		c.emit(ctx, e)
		c.resolve(ctx, ipKey{h.ctx.id, b.ip}, t, causeHostCap, 0, ev, h)
	}
	if len(h.names) > 0 {
		var rs []*record
		for _, r := range c.st.records {
			if r.key.hostID == h.id {
				rs = append(rs, r)
			}
		}
		slices.SortFunc(rs, func(a, b *record) int { return cmp.Compare(a.id, b.id) })
		touched := map[*host]bool{}
		for _, r := range rs {
			c.endRecord(ctx, r, t, endHostEvicted, 0, ev, touched)
		}
	}
	c.st.removeHost(h)
	h.ctx.evicted++
	h.ctx.caps.evicted.Add(1)
	c.exec(ctx, "evict host", `UPDATE hosts SET evicted_at = ? WHERE host_id = ?`, ms(t), h.id)
	c.holdPending(ctx, store.EvictedHost{
		HostID: h.id, Interface: h.ctx.iface, MAC: h.mac.String(), ContextID: h.ctx.id, OUIRegistrant: h.vendor,
		PreferredName: h.preferred, Description: h.description, ProxyARP: h.proxyARP, FirstSeen: ms(h.firstSeen), LastSeen: ms(h.lastSeen),
		LastFrameSeen: optToMS(h.lastFrame), LastAddressConfirmed: optToMS(h.lastAddress), LastEndpointAnswer: optToMS(h.lastAnswer),
	})
	e := hostEvent(events.HostEvicted, t, h, causeHostCap, ev)
	e.Old, e.RelatedMAC = string(h.presence), mac.String()
	c.emit(ctx, e)
}

// reinstate holds an evicted host of n again under its host_id, from row
// (a host is one MAC per interface, never split: AGENTS.md rule 6). The
// observation that brought it back makes it present, if it is presence
// evidence (bindMAC).
func (c *Correlator) reinstate(ctx context.Context, n *netContext, mac net.HardwareAddr, row store.EvictedHost) {
	h := newHost()
	h.id, h.vendor, h.preferred, h.proxyARP, h.description = row.HostID, row.OUIRegistrant, row.PreferredName, row.ProxyARP, row.Description
	h.ctx, h.mac, h.presence = n, append(net.HardwareAddr(nil), mac...), Missing
	h.firstSeen, h.lastSeen = fromMS(row.FirstSeen), fromMS(row.LastSeen)
	h.lastFrame, h.lastAddress, h.lastAnswer = optMS(row.LastFrameSeen), optMS(row.LastAddressConfirmed), optMS(row.LastEndpointAnswer)
	h.evidence = events.Evidence{TS: h.lastSeen, Interface: n.iface, MAC: row.MAC}
	c.st.dropPending(row.HostID)
	c.st.addHost(h)
	n.evicted--
	c.exec(ctx, "reinstate host", `UPDATE hosts SET evicted_at = NULL WHERE host_id = ?`, h.id)
}

// evictedRow finds mac among n's evicted hosts. A host evicted or
// described since the writer last committed its writes is found in
// memory (pending); any other through the read-only reader, which sees
// what is committed. Neither makes the writer commit, so the batching of
// writes holds however many evicted MACs return (NFR-PERF-2).
func (c *Correlator) evictedRow(ctx context.Context, n *netContext, mac net.HardwareAddr) (store.EvictedHost, bool, error) {
	if n.evicted == 0 {
		return store.EvictedHost{}, false, nil
	}
	c.prunePending()
	key := mac.String()
	if p := c.st.pendingByMAC[evictedKey{n.id, key}]; p != nil {
		return p.row, true, nil
	}
	row, err := c.reader.HostRow(ctx, n.id, key)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return row, false, nil
	case err != nil:
		return row, false, err
	}
	return row, true, nil
}

// evictedKey is an evicted host by context and MAC.
type evictedKey struct {
	ctx int64
	mac string
}

// pendingRow is the newest row of an evicted host whose writes the writer
// may not have committed yet. batch is set when the writer runs the
// marker queued after those writes: the batch that commits them.
type pendingRow struct {
	row   store.EvictedHost
	batch atomic.Uint64
}

// holdPending keeps row in memory until the writes queued before it are
// committed; until then the reader may not see them.
func (c *Correlator) holdPending(ctx context.Context, row store.EvictedHost) {
	c.prunePending()
	p := &pendingRow{row: row}
	c.st.dropPending(row.HostID)
	c.st.pendingByMAC[evictedKey{row.ContextID, row.MAC}] = p
	c.st.pendingByID[row.HostID] = p
	c.st.pendingQ = append(c.st.pendingQ, p)
	// The marker runs in the batch that commits the writes queued before
	// it; that batch is the next to be counted by Commits.
	err := c.store.Submit(ctx, func(context.Context, *sql.Tx) error {
		p.batch.Store(c.store.Commits() + 1)
		return nil
	})
	if err != nil && ctx.Err() == nil {
		c.log.Error("correlator: queueing write failed", "what", "evicted host marker", "err", err)
	}
}

// prunePending forgets the pending rows the writer has committed. Markers
// run in the order they were queued, so the committed ones are at the
// front of the queue.
func (c *Correlator) prunePending() {
	done := c.store.Commits()
	q := c.st.pendingQ
	i := 0
	for ; i < len(q); i++ {
		b := q[i].batch.Load()
		if b == 0 || b > done {
			break
		}
		if p := c.st.pendingByID[q[i].row.HostID]; p == q[i] {
			c.st.dropPending(p.row.HostID)
		}
	}
	c.st.pendingQ = slices.Delete(q, 0, i)
}

// dropPending forgets the pending row of a host, if any (its queue entry
// goes at the next prune).
func (s *state) dropPending(hostID string) {
	if p := s.pendingByID[hostID]; p != nil {
		delete(s.pendingByID, hostID)
		delete(s.pendingByMAC, evictedKey{p.row.ContextID, p.row.MAC})
	}
}

// optToMS writes an optional time as stored: 0 when unknown.
func optToMS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return ms(t)
}

// leaseQueue orders a context's open DHCP leases by last ACK, then id.
func (n *netContext) leaseQueue() *pq[*dhcpLease] {
	if n.leaseQ.less == nil {
		n.leaseQ.less = func(a, b *dhcpLease) bool {
			return a.lastAck.Before(b.lastAck) || a.lastAck.Equal(b.lastAck) && a.id < b.id
		}
		n.leaseQ.pos = func(l *dhcpLease) *int { return &l.pos }
	}
	return &n.leaseQ
}

// limitLeases makes room on n for a new client's lease: at the cap, the
// lease with the oldest ACK ends "evicted" (no event; the lease history
// keeps it).
func (c *Correlator) limitLeases(ctx context.Context, n *netContext, o observation.Observation) {
	q := n.leaseQueue()
	for limit := c.hostCap(); q.Len() >= limit; {
		l, _ := q.peek()
		if c.st.leases[l.key] != l || l.key.ctx != n.id {
			// Not the open lease of its key on n: endLease would not take
			// it off this queue, and the loop would never end.
			q.remove(l)
			continue
		}
		c.endLease(ctx, l.key, o.Time, endLeaseEvicted)
		n.caps.leasesEvicted.Add(1)
	}
}
