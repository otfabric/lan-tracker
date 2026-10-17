package correlate

import (
	"context"
	"database/sql"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/observation"
)

// nameTypes are the name types the schema accepts (name_records.name_type).
var nameTypes = []observation.NameType{
	observation.NameMDNS, observation.NameDHCP, observation.NameDNS, observation.NameDNSPTR, observation.NameNetBIOS, observation.NameLLDP,
}

func validNameType(t observation.NameType) bool { return slices.Contains(nameTypes, t) }

// Name record links (name_records.link).
const (
	linkSender  = "sender"  // a name the device gave itself
	linkAddress = "address" // one MAC alone held the address the record describes
	linkNone    = "none"
	linkLegacy  = "legacy" // LAN Sentinel's names, attached to the host they were found on
)

// Reasons a name record ends (name_records.end_reason).
const (
	endGoodbye    = "goodbye"     // mDNS TTL 0 (RFC 6762 §10.1)
	endCacheFlush = "cache_flush" // replaced by a cache-flush record (RFC 6762 §10.2)
	endRelinked   = "address now held by another MAC"
	endEvicted    = "evicted"
)

// maxOpenRecords bounds the open name records kept in memory: a LAN that
// advertises more has the least recently seen ones ended (no event).
const maxOpenRecords = 16384

// recordKey identifies an open name record. A NAME record has no
// advertiser apart from its host (the device named itself).
type recordKey struct {
	ctx        int64
	typ        observation.RecordType
	name       string // lower case
	addr       netip.Addr
	value      string
	source     observation.Source
	hostID     string
	advertiser string // the sender's MAC
}

// sameRecord is a record's identity without its link: what goodbye, cache
// flush and relinking compare.
func (k recordKey) sameRecord(o recordKey) bool {
	return k.ctx == o.ctx && k.typ == o.typ && k.name == o.name && k.addr == o.addr && k.value == o.value &&
		k.source == o.source && k.advertiser == o.advertiser
}

type record struct {
	id                  int64
	key                 recordKey
	name                string // as advertised
	nameType            observation.NameType
	link                string
	firstSeen, lastSeen time.Time
	legacy              bool
}

// hostName is a name of a host while records of it are open.
type hostName struct {
	count     int
	firstSeen time.Time
}

// nameTypeOf is the name type of a record from src: what
// identity.hostname_preference orders.
func nameTypeOf(src observation.Source, typ observation.RecordType, given observation.NameType) observation.NameType {
	switch src {
	case observation.PassiveMDNS, observation.MDNSQuery:
		return observation.NameMDNS
	case observation.PassiveDNS, observation.DNSQuery:
		if typ == observation.RecordPTR {
			return observation.NameDNSPTR
		}
		return observation.NameDNS
	case observation.PassiveDHCP:
		return observation.NameDHCP
	case observation.PassiveLLDP:
		return observation.NameLLDP
	}
	return given
}

// recordsOf returns o's name records. An observation in the older form,
// a hostname with a name type (JSONL replay files, golden scenarios), is
// read as the record it stood for: an address record for mDNS and DNS
// names with an address, else a NAME the sender gave itself.
func recordsOf(o observation.Observation) []observation.Record {
	out := o.Records
	if o.Hostname == "" || !validNameType(o.NameType) {
		return out
	}
	r := observation.Record{Name: o.Hostname, Type: observation.RecordName}
	switch {
	case o.NameType == observation.NameDNSPTR && o.IP.IsValid():
		r.Type, r.Addr = observation.RecordPTR, o.IP
	case (o.NameType == observation.NameMDNS || o.NameType == observation.NameDNS) && o.IP.IsValid():
		r.Type, r.Addr = observation.RecordA, o.IP
		if o.IP.Is6() {
			r.Type = observation.RecordAAAA
		}
	}
	return append(slices.Clone(out), r)
}

// names applies docs/DATA_MODEL.md §5.4 to the name records of o, seen on
// n from sender (the host of o's MAC, nil without one). Every record is
// kept with its source, advertiser and TTL. It is linked to a host only by
// evidence: a NAME to its sender, an address record to the one MAC that
// holds the address (a proxy or reflector advertising others' addresses
// binds nothing to itself); otherwise it stays unlinked. A host may have
// several names of a type: a new linked name is HOSTNAME_ADDED, and when
// the last record of a name ends, HOSTNAME_REMOVED. Records end only by
// evidence: an mDNS goodbye (TTL 0) one second later, a cache-flush record
// replacing older ones (RFC 6762 §10), or the address passing to another
// MAC; a TTL running out only makes the record expired when read, and time
// alone never ends a name.
func (c *Correlator) names(ctx context.Context, n *netContext, sender *host, o observation.Observation, obsID int64, ev events.Evidence) {
	records := recordsOf(o)
	if len(records) == 0 {
		return
	}
	advertiser := ""
	if len(o.MAC) > 0 {
		advertiser = o.MAC.String()
	}
	mdns := o.Source == observation.PassiveMDNS || o.Source == observation.MDNSQuery
	touched := map[*host]bool{}
	for _, r := range records {
		nt := nameTypeOf(o.Source, r.Type, o.NameType)
		if r.Name == "" || !validNameType(nt) {
			continue
		}
		var linked *host
		link := linkNone
		switch {
		case r.Type == observation.RecordName:
			if linked = sender; linked != nil {
				link = linkSender
			}
		case r.Addr.IsValid() && (r.Type == observation.RecordA || r.Type == observation.RecordAAAA || r.Type == observation.RecordPTR):
			if hh := c.st.soleHolder(ipKey{n.id, r.Addr.Unmap()}, o.Time, c.cfg.Load().Identity.AddressExpiry.D()); hh != nil {
				linked, link = hh, linkAddress
			}
		}
		k := recordKey{ctx: n.id, typ: r.Type, name: strings.ToLower(r.Name), addr: r.Addr.Unmap(), value: r.Value,
			source: o.Source, advertiser: advertiser}
		if r.Type == observation.RecordName {
			k.advertiser = ""
		}
		if linked != nil {
			k.hostID = linked.id
		}
		if mdns && r.TTL != nil && *r.TTL == 0 {
			// A goodbye: the record leaves the caches one second later.
			for _, x := range c.matching(func(x *record) bool { return x.key.sameRecord(k) }) {
				c.endRecord(ctx, x, o.Time.Add(time.Second), endGoodbye, obsID, ev, touched)
			}
			continue
		}
		// A cache-flush record replaces the older records of its name and
		// type. They end after the record is counted, so a name moving to a
		// new address is never reported as removed and added again.
		var flushed []*record
		if mdns && r.Flush {
			since := o.Time.Add(-time.Second)
			flushed = c.matching(func(x *record) bool {
				return x.key.ctx == k.ctx && x.key.typ == k.typ && x.key.name == k.name && x.key.source == k.source &&
					(x.key.addr != k.addr || x.key.value != k.value) && x.lastSeen.Before(since)
			})
		}
		c.openOrRefresh(ctx, k, r, o, nt, link, linked, obsID, ev, touched)
		for _, x := range flushed {
			c.endRecord(ctx, x, o.Time, endCacheFlush, obsID, ev, touched)
		}
	}
	for h := range touched {
		c.prefer(ctx, h)
	}
}

// openOrRefresh refreshes the open record k, or opens it, ending first the
// link of the same record to a MAC that no longer holds the address alone.
func (c *Correlator) openOrRefresh(ctx context.Context, k recordKey, r observation.Record, o observation.Observation, nt observation.NameType,
	link string, linked *host, obsID int64, ev events.Evidence, touched map[*host]bool) {
	if rec := c.st.records[k]; rec != nil {
		if o.Time.After(rec.lastSeen) {
			rec.lastSeen = o.Time
		}
		c.dbRecordSeen(ctx, rec, r, o, link)
		rec.link, rec.legacy = link, false
		return
	}
	if link == linkAddress {
		// The record linked to a MAC that no longer holds the address
		// alone: that link ends.
		for _, x := range c.matching(func(x *record) bool {
			return x.key.sameRecord(k) && x.link == linkAddress && x.key.hostID != k.hostID
		}) {
			c.endRecord(ctx, x, o.Time, endRelinked, obsID, ev, touched)
		}
	}
	c.st.nextRecordID++
	rec := &record{id: c.st.nextRecordID, key: k, name: r.Name, nameType: nt, link: link, firstSeen: o.Time, lastSeen: o.Time}
	c.st.records[k] = rec
	c.dbOpenRecord(ctx, rec, r, o)
	if linked != nil {
		c.nameOpened(ctx, linked, rec, obsID, ev)
		touched[linked] = true
	}
	c.evictRecords(ctx, o.Time, touched)
}

// matching returns the open records for which keep holds, in id order.
func (c *Correlator) matching(keep func(*record) bool) []*record {
	var out []*record
	for _, x := range c.st.records {
		if keep(x) {
			out = append(out, x)
		}
	}
	slices.SortFunc(out, func(a, b *record) int { return int(a.id - b.id) })
	return out
}

func hostNameKey(r *record) string { return string(r.nameType) + ":" + r.name }

// nameOpened counts a linked record on its host: the host's first record
// of the name emits HOSTNAME_ADDED.
func (c *Correlator) nameOpened(ctx context.Context, h *host, r *record, obsID int64, ev events.Evidence) {
	key := hostNameKey(r)
	cur := h.names[key]
	if cur.count == 0 {
		cur.firstSeen = r.firstSeen
		e := hostEvent(events.HostnameAdded, r.firstSeen, h, string(r.key.source), ev)
		e.New, e.ObservationID = key, obsID
		c.emit(ctx, e)
	}
	cur.count++
	h.names[key] = cur
}

// endRecord ends an open record at t. When it was its host's last record
// of the name, HOSTNAME_REMOVED says why.
func (c *Correlator) endRecord(ctx context.Context, r *record, t time.Time, reason string, obsID int64, ev events.Evidence, touched map[*host]bool) {
	delete(c.st.records, r.key)
	if t.Before(r.firstSeen) {
		t = r.firstSeen
	}
	c.exec(ctx, "end name record", `UPDATE name_records SET ended_at = ?, end_reason = ? WHERE id = ?`, ms(t), reason, r.id)
	if r.key.hostID == "" {
		return
	}
	h := c.hostByID(r.key.hostID)
	if h == nil {
		return
	}
	key := hostNameKey(r)
	cur := h.names[key]
	if cur.count--; cur.count > 0 {
		h.names[key] = cur
		return
	}
	delete(h.names, key)
	touched[h] = true
	if reason == endEvicted {
		return
	}
	ev.Reason = reason
	e := hostEvent(events.HostnameRemoved, t, h, string(r.key.source), ev)
	e.Old, e.ObservationID = key, obsID
	c.emit(ctx, e)
}

// evictRecords ends the least recently seen records beyond maxOpenRecords.
func (c *Correlator) evictRecords(ctx context.Context, t time.Time, touched map[*host]bool) {
	for len(c.st.records) > maxOpenRecords {
		var oldest *record
		for _, r := range c.st.records {
			if oldest == nil || r.lastSeen.Before(oldest.lastSeen) || r.lastSeen.Equal(oldest.lastSeen) && r.id < oldest.id {
				oldest = r
			}
		}
		c.endRecord(ctx, oldest, t, endEvicted, 0, events.Evidence{}, touched)
	}
}

// prefer recomputes preferred_name: of the host's names, those of the
// first type in identity.hostname_preference it has, the one it has had
// longest (FR-NM-2). It is a display choice: it never changes the host's
// identity or its description, and types missing from the list are never
// preferred.
func (c *Correlator) prefer(ctx context.Context, h *host) {
	preferred := ""
	for _, t := range c.cfg.Load().Identity.HostnamePreference {
		var best string
		var bestSince time.Time
		for key, n := range h.names {
			typ, name, _ := strings.Cut(key, ":")
			if typ != t {
				continue
			}
			if best == "" || n.firstSeen.Before(bestSince) || n.firstSeen.Equal(bestSince) && name < best {
				best, bestSince = name, n.firstSeen
			}
		}
		if best != "" {
			preferred = best
			break
		}
	}
	if preferred != h.preferred {
		h.preferred = preferred
		c.dbPreferred(ctx, h)
	}
}

func recordTTL(r observation.Record) any {
	if r.TTL == nil {
		return nil
	}
	return int64(*r.TTL)
}

func recordExpiry(r observation.Record, t time.Time) any {
	if r.TTL == nil {
		return nil
	}
	return ms(t.Add(time.Duration(*r.TTL) * time.Second))
}

func addrText(a netip.Addr) any {
	if !a.IsValid() {
		return nil
	}
	return a.String()
}

func (c *Correlator) dbOpenRecord(ctx context.Context, rec *record, r observation.Record, o observation.Observation) {
	k := rec.key
	advertiser, advertiserIP := any(nil), any(nil)
	if len(o.MAC) > 0 {
		advertiser = o.MAC.String()
	}
	if o.IP.IsValid() {
		advertiserIP = o.IP.String()
	}
	c.exec(ctx, "open name record", `INSERT INTO name_records (id, context_id, name, name_type, record_type, address, value, source,
		advertiser_mac, advertiser_ip, host_id, link, ttl_seconds, first_seen, last_seen, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.id, k.ctx, rec.name, string(rec.nameType), string(k.typ), addrText(k.addr), nullString(k.value), string(k.source),
		advertiser, advertiserIP, nullString(k.hostID), rec.link, recordTTL(r), ms(rec.firstSeen), ms(rec.lastSeen),
		recordExpiry(r, o.Time))
}

func (c *Correlator) dbRecordSeen(ctx context.Context, rec *record, r observation.Record, o observation.Observation, link string) {
	advertiser, advertiserIP := any(nil), any(nil)
	if len(o.MAC) > 0 {
		advertiser = o.MAC.String()
	}
	if o.IP.IsValid() {
		advertiserIP = o.IP.String()
	}
	c.exec(ctx, "update name record", `UPDATE name_records SET last_seen = max(last_seen, ?), ttl_seconds = ?, expires_at = ?,
		advertiser_mac = coalesce(?, advertiser_mac), advertiser_ip = coalesce(?, advertiser_ip), link = ?, legacy = 0 WHERE id = ?`,
		ms(rec.lastSeen), recordTTL(r), recordExpiry(r, rec.lastSeen), advertiser, advertiserIP, link, rec.id)
}

func (c *Correlator) dbPreferred(ctx context.Context, h *host) {
	c.exec(ctx, "preferred name", `UPDATE hosts SET preferred_name = ? WHERE host_id = ?`, nullString(h.preferred), h.id)
}

// loadRecords reads the open name records and counts the hosts' names.
func (s *state) loadRecords(ctx context.Context, tx *sql.Tx, hosts map[string]*host) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, context_id, name, name_type, record_type, coalesce(address, ''), coalesce(value, ''),
		source, coalesce(advertiser_mac, ''), coalesce(host_id, ''), link, first_seen, last_seen, legacy
		FROM name_records WHERE ended_at IS NULL ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		r := &record{}
		var nt, typ, addr, src string
		var first, last int64
		if err := rows.Scan(&r.id, &r.key.ctx, &r.name, &nt, &typ, &addr, &r.key.value, &src, &r.key.advertiser, &r.key.hostID,
			&r.link, &first, &last, &r.legacy); err != nil {
			return err
		}
		if addr != "" {
			if r.key.addr, err = netip.ParseAddr(addr); err != nil {
				return err
			}
		}
		r.nameType, r.key.typ, r.key.source = observation.NameType(nt), observation.RecordType(typ), observation.Source(src)
		r.key.name, r.firstSeen, r.lastSeen = strings.ToLower(r.name), fromMS(first), fromMS(last)
		if r.key.typ == observation.RecordName {
			r.key.advertiser = ""
		}
		s.records[r.key] = r
		if h := hosts[r.key.hostID]; h != nil {
			cur := h.names[hostNameKey(r)]
			if cur.count == 0 || r.firstSeen.Before(cur.firstSeen) {
				cur.firstSeen = r.firstSeen
			}
			cur.count++
			h.names[hostNameKey(r)] = cur
		}
	}
	return rows.Err()
}
