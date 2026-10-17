package correlate

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/otfabric/lan-tracker/internal/events"
)

// Presence is a host's presence state (docs/DATA_MODEL.md §6).
type Presence string

// Presence states.
const (
	Active  Presence = "ACTIVE"
	Recent  Presence = "RECENT"
	Stale   Presence = "STALE"
	Missing Presence = "MISSING"
)

// live reports whether a host in this state can hold an IP against a new
// claimant (ACTIVE or RECENT).
func (p Presence) live() bool { return p == Active || p == Recent }

type netContext struct {
	id       int64
	iface    string
	prefixes map[netip.Prefix]*prefixRow // open context_prefixes
	up       *bool                       // nil until the first link state
	lastSeen time.Time
	dirty    bool // lastSeen not yet written
}

type prefixRow struct {
	id        int64
	firstSeen time.Time
}

type host struct {
	id        string
	ctx       *netContext
	mac       net.HardwareAddr
	vendor    string
	presence  Presence
	firstSeen time.Time
	// lastSeen is the latest presence evidence of any kind; the kinds are
	// also kept apart (docs/DATA_MODEL.md §2): a frame from the MAC, an
	// address confirmed at it, an endpoint it held alone that answered.
	lastSeen, lastFrame, lastAddress, lastAnswer time.Time
	lastChange                                   time.Time // latest binding open/close on this host
	evidence                                     events.Evidence
	bindings                                     map[netip.Addr]*binding // open bindings
	// names counts the open name records linked to the host per name
	// ("type:name"): HOSTNAME_ADDED when one appears, HOSTNAME_REMOVED when
	// the last record of it ends.
	names     map[string]hostName
	preferred string

	description string // set by an operator (§5.8)

	// Proxy-ARP detection (docs/DATA_MODEL.md §5.2): when this MAC last
	// answered ARP for each address, and for those held by another live host.
	proxyARP    bool
	arpClaims   map[netip.Addr]time.Time
	arpOverlaps map[netip.Addr]time.Time
}

func newHost() *host {
	return &host{
		bindings: map[netip.Addr]*binding{}, names: map[string]hostName{},
		arpClaims: map[netip.Addr]time.Time{}, arpOverlaps: map[netip.Addr]time.Time{},
	}
}

// soleHolder returns the host that alone holds k at t, if its binding was
// confirmed within expiry before t: a MAC-less observation (a TCP result,
// an address record) is attributed only to a fresh, unambiguous holder
// (docs/DATA_MODEL.md §5.4, §5.5). A binding nobody confirmed for that long
// may belong to a device that left; the observation stays unattributed.
func (s *state) soleHolder(k ipKey, t time.Time, expiry time.Duration) *host {
	hs := s.holders[k]
	if len(hs) != 1 || t.Sub(hs[0].lastSeen) > expiry {
		return nil
	}
	return hs[0].host
}

type binding struct {
	id        int64
	host      *host
	ip        netip.Addr
	conflict  bool
	firstSeen time.Time
	lastSeen  time.Time
	// arpOnly: opened by an ARP reply and not confirmed by other evidence
	// since (bindings loaded at start-up count as confirmed).
	arpOnly bool
}

// ipKey identifies an IP within a context.
type ipKey struct {
	ctx int64
	ip  netip.Addr
}

// state is the correlator's in-memory current state. Only the correlator
// goroutine touches it.
type state struct {
	contexts     map[string]*netContext
	contextsByID map[int64]*netContext
	hosts        map[int64]map[string]*host // context id → MAC → host
	hostsByMAC   map[string][]*host         // MAC → hosts on every context
	order        []*host                    // creation order, for deterministic timers
	holders      map[ipKey][]*binding       // open bindings per IP
	ipLastChange map[ipKey]time.Time

	dhcpServers map[dhcpKey]*dhcpServer   // §5.6
	endpoints   map[endpointKey]*endpoint // §5.5
	records     map[recordKey]*record     // open name records, §5.4
	exchanges   map[exchKey]*exchange     // open DHCP exchanges, §5.6
	leases      map[leaseKey]*dhcpLease   // clients' open leases, §5.6
	// exchangesSwept is when idle exchanges were last closed.
	exchangesSwept time.Time

	nextContextID, nextPrefixID, nextAddressID, nextObservationID, nextScanID int64

	nextDHCPServerID, nextEndpointID, nextEndpointResultID, nextRecordID, nextExchangeID, nextLeaseID int64

	openScans []openScan // left unfinished by the previous run
}

type openScan struct {
	id, ctx int64
	kind    string
}

func newState() *state {
	return &state{
		contexts: map[string]*netContext{}, contextsByID: map[int64]*netContext{},
		hosts: map[int64]map[string]*host{}, hostsByMAC: map[string][]*host{},
		holders: map[ipKey][]*binding{}, ipLastChange: map[ipKey]time.Time{}, dhcpServers: map[dhcpKey]*dhcpServer{},
		endpoints: map[endpointKey]*endpoint{}, records: map[recordKey]*record{},
		exchanges: map[exchKey]*exchange{}, leases: map[leaseKey]*dhcpLease{},
	}
}

func (s *state) addHost(h *host) {
	if s.hosts[h.ctx.id] == nil {
		s.hosts[h.ctx.id] = map[string]*host{}
	}
	key := h.mac.String()
	s.hosts[h.ctx.id][key] = h
	s.hostsByMAC[key] = append(s.hostsByMAC[key], h)
	s.order = append(s.order, h)
}

func (s *state) addBinding(b *binding) {
	b.host.bindings[b.ip] = b
	k := ipKey{b.host.ctx.id, b.ip}
	s.holders[k] = append(s.holders[k], b)
}

func (s *state) removeBinding(b *binding) {
	delete(b.host.bindings, b.ip)
	k := ipKey{b.host.ctx.id, b.ip}
	hs := s.holders[k]
	for i, x := range hs {
		if x == b {
			hs = append(hs[:i], hs[i+1:]...)
			break
		}
	}
	if len(hs) == 0 {
		delete(s.holders, k)
	} else {
		s.holders[k] = hs
	}
}

func (s *state) touchIP(k ipKey, t time.Time) {
	if t.After(s.ipLastChange[k]) {
		s.ipLastChange[k] = t
	}
}

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

// optMS reads an optional time stored as 0 when unknown.
func optMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return fromMS(v)
}

// msOrNil writes an optional time: NULL when unknown.
func msOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return ms(t)
}

// load reads the current state from the database (open bindings only).
func (s *state) load(ctx context.Context, tx *sql.Tx) error {
	var err error
	q := func(query string, scan func(*sql.Rows) error) {
		if err != nil {
			return
		}
		var rows *sql.Rows
		rows, err = tx.QueryContext(ctx, query)
		if err != nil {
			err = fmt.Errorf("load state: %w", err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			if err = scan(rows); err != nil {
				err = fmt.Errorf("load state: %w", err)
				return
			}
		}
		if err == nil {
			err = rows.Err()
		}
	}

	q(`SELECT id, interface, last_seen FROM network_contexts`, func(r *sql.Rows) error {
		c := &netContext{prefixes: map[netip.Prefix]*prefixRow{}}
		var last int64
		if err := r.Scan(&c.id, &c.iface, &last); err != nil {
			return err
		}
		c.lastSeen = fromMS(last)
		s.contexts[c.iface], s.contextsByID[c.id] = c, c
		return nil
	})
	q(`SELECT id, context_id, prefix, first_seen FROM context_prefixes WHERE ended_at IS NULL`, func(r *sql.Rows) error {
		var id, cid, first int64
		var text string
		if err := r.Scan(&id, &cid, &text, &first); err != nil {
			return err
		}
		p, err := netip.ParsePrefix(text)
		if err != nil {
			return err
		}
		if c := s.contextsByID[cid]; c != nil {
			c.prefixes[p] = &prefixRow{id: id, firstSeen: fromMS(first)}
		}
		return nil
	})
	byID := map[string]*host{}
	q(`SELECT host_id, context_id, mac, coalesce(oui_registrant, ''), presence, coalesce(preferred_name, ''), proxy_arp,
		coalesce(description, ''), first_seen, last_seen, coalesce(last_frame_seen, 0), coalesce(last_address_confirmed, 0),
		coalesce(last_endpoint_answer, 0) FROM hosts ORDER BY first_seen, host_id`, func(r *sql.Rows) error {
		h := newHost()
		var cid, first, last, frame, addr, answer int64
		var mac, presence string
		if err := r.Scan(&h.id, &cid, &mac, &h.vendor, &presence, &h.preferred, &h.proxyARP, &h.description, &first, &last,
			&frame, &addr, &answer); err != nil {
			return err
		}
		h.lastFrame, h.lastAddress, h.lastAnswer = optMS(frame), optMS(addr), optMS(answer)
		var err error
		if h.mac, err = net.ParseMAC(mac); err != nil {
			return err
		}
		h.ctx = s.contextsByID[cid]
		if h.ctx == nil {
			return fmt.Errorf("host %s references unknown context %d", h.id, cid)
		}
		h.presence, h.firstSeen, h.lastSeen = Presence(presence), fromMS(first), fromMS(last)
		h.evidence = events.Evidence{TS: h.lastSeen, Interface: h.ctx.iface, MAC: mac}
		s.addHost(h)
		byID[h.id] = h
		return nil
	})
	q(`SELECT id, host_id, ip, conflict, first_seen, last_seen FROM addresses WHERE ended_at IS NULL`, func(r *sql.Rows) error {
		b := &binding{}
		var hid, ip string
		var conflict int
		var first, last int64
		if err := r.Scan(&b.id, &hid, &ip, &conflict, &first, &last); err != nil {
			return err
		}
		var err error
		if b.ip, err = netip.ParseAddr(ip); err != nil {
			return err
		}
		if b.host = byID[hid]; b.host == nil {
			return fmt.Errorf("address %d references unknown host %s", b.id, hid)
		}
		b.conflict, b.firstSeen, b.lastSeen = conflict == 1, fromMS(first), fromMS(last)
		s.addBinding(b)
		return nil
	})
	q(`SELECT host_id, context_id, ip, max(max(first_seen), max(coalesce(ended_at, 0))) FROM addresses GROUP BY host_id, context_id, ip`, func(r *sql.Rows) error {
		var hid, ip string
		var cid, t int64
		if err := r.Scan(&hid, &cid, &ip, &t); err != nil {
			return err
		}
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			return err
		}
		s.touchIP(ipKey{cid, addr}, fromMS(t))
		if h := byID[hid]; h != nil && fromMS(t).After(h.lastChange) {
			h.lastChange = fromMS(t)
		}
		return nil
	})
	q(`SELECT id, context_id, kind FROM scans WHERE finished_at IS NULL ORDER BY id`, func(r *sql.Rows) error {
		var sc openScan
		if err := r.Scan(&sc.id, &sc.ctx, &sc.kind); err != nil {
			return err
		}
		s.openScans = append(s.openScans, sc)
		return nil
	})
	for _, load := range []func(context.Context, *sql.Tx) error{s.loadDHCPServers, s.loadLeases, s.loadEndpoints,
		func(ctx context.Context, tx *sql.Tx) error { return s.loadRecords(ctx, tx, byID) }} {
		if err == nil {
			if err = load(ctx, tx); err != nil {
				err = fmt.Errorf("load state: %w", err)
			}
		}
	}
	for _, c := range []struct {
		table string
		dst   *int64
	}{
		{"network_contexts", &s.nextContextID}, {"context_prefixes", &s.nextPrefixID},
		{"addresses", &s.nextAddressID}, {"observations", &s.nextObservationID}, {"scans", &s.nextScanID},
		{"endpoints", &s.nextEndpointID}, {"endpoint_results", &s.nextEndpointResultID}, {"name_records", &s.nextRecordID},
		{"dhcp_exchanges", &s.nextExchangeID}, {"dhcp_leases", &s.nextLeaseID},
	} {
		q(`SELECT coalesce(max(id), 0) FROM `+c.table, func(r *sql.Rows) error { return r.Scan(c.dst) })
	}
	return err
}
