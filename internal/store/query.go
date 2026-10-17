package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned for an unknown host ID.
var ErrNotFound = errors.New("not found")

// ErrBadQuery is returned when a query does not match its explicit kind.
var ErrBadQuery = errors.New("invalid query")

func msOf(t time.Time) int64 { return t.UnixMilli() }

func timeOf(v int64) time.Time { return time.UnixMilli(v).UTC() }

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := timeOf(v.Int64)
	return &t
}

// where accumulates SQL conditions and their arguments.
type where struct {
	conds []string
	args  []any
}

func (w *where) add(cond string, args ...any) {
	w.conds = append(w.conds, cond)
	w.args = append(w.args, args...)
}

func (w *where) sql() string {
	if len(w.conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(w.conds, " AND ")
}

const summaryColumns = `h.host_id, c.interface, h.mac, coalesce(h.oui_registrant, ''), h.locally_administered, h.presence,
	coalesce(h.preferred_name, ''), h.proxy_arp, coalesce(h.description, ''), h.first_seen, h.last_seen,
	h.last_frame_seen, h.last_address_confirmed, h.last_endpoint_answer`

func scanSummary(rows interface{ Scan(...any) error }) (HostSummary, error) {
	var h HostSummary
	var la int
	var first, last int64
	var frame, addr, answer sql.NullInt64
	err := rows.Scan(&h.HostID, &h.Interface, &h.MAC, &h.Vendor, &la, &h.Presence, &h.PreferredName, &h.ProxyARP,
		&h.Description, &first, &last, &frame, &addr, &answer)
	h.LocallyAdministered, h.FirstSeen, h.LastSeen, h.IPs = la == 1, timeOf(first), timeOf(last), []string{}
	h.LastFrameSeen, h.LastAddressConfirmed, h.LastEndpointAnswer = timePtr(frame), timePtr(addr), timePtr(answer)
	return h, err
}

// sortIPs orders addresses numerically, IPv4 first.
func sortIPs(ips []string) {
	sort.Slice(ips, func(i, j int) bool {
		a, errA := netip.ParseAddr(ips[i])
		b, errB := netip.ParseAddr(ips[j])
		if errA != nil || errB != nil {
			return ips[i] < ips[j]
		}
		return a.Less(b)
	})
}

// openIPs returns the open bindings of hosts, keyed by host ID.
func openIPs(ctx context.Context, tx *sql.Tx, hostIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(hostIDs) == 0 {
		return out, nil
	}
	// One JSON parameter instead of one per host: SQLite limits bound
	// variables to 32766, and hosts are never pruned.
	ids, err := json.Marshal(hostIDs)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT host_id, ip FROM addresses
		WHERE ended_at IS NULL AND host_id IN (SELECT value FROM json_each(?))`, string(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, ip string
		if err := rows.Scan(&id, &ip); err != nil {
			return nil, err
		}
		out[id] = append(out[id], ip)
	}
	for _, ips := range out {
		sortIPs(ips)
	}
	return out, rows.Err()
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

func anys[T any](v []T) []any {
	out := make([]any, len(v))
	for i := range v {
		out[i] = v[i]
	}
	return out
}

// matchCondition restricts hosts h to those that ever matched a query.
func matchCondition(w *where, kind QueryKind, q string) {
	switch kind {
	case KindHost:
		w.add(`h.host_id = ?`, q)
	case KindMAC:
		w.add(`h.mac = ?`, q)
	case KindIP:
		w.add(`EXISTS (SELECT 1 FROM addresses a WHERE a.host_id = h.host_id AND a.ip = ?)`, q)
	case KindName:
		// preferred_name is always one of the host's names.
		w.add(`h.host_id IN (SELECT n.host_id FROM name_records n WHERE n.name = ? COLLATE NOCASE)`, q)
	}
}

// hostWhere builds the conditions of a host filter.
func hostWhere(f HostFilter) (where, error) {
	var w where
	if f.Interface != "" {
		w.add(`c.interface = ?`, f.Interface)
	}
	if f.Query != "" {
		kind, q, ok := Normalize(f.QueryKind, f.Query)
		if !ok {
			return w, fmt.Errorf("%w: %q is not a %s", ErrBadQuery, f.Query, f.QueryKind)
		}
		matchCondition(&w, kind, q)
	}
	switch {
	case f.Live && !f.NotLive:
		w.add(`h.presence IN ('ACTIVE', 'RECENT')`)
	case f.NotLive && !f.Live:
		w.add(`h.presence IN ('STALE', 'MISSING')`)
	}
	if f.Vendor != "" {
		w.add(`instr(lower(coalesce(h.oui_registrant, '')), lower(?)) > 0`, f.Vendor)
	}
	if f.Description != "" {
		w.add(`instr(lower(coalesce(h.description, '')), lower(?)) > 0`, f.Description)
	}
	if f.Port > 0 {
		w.add(`EXISTS (SELECT 1 FROM endpoints e WHERE e.host_id = h.host_id AND e.port = ? AND e.last_definite_result = 'OPEN')`, f.Port)
	}
	if !f.SeenSince.IsZero() {
		w.add(`h.last_seen >= ?`, msOf(f.SeenSince))
	}
	return w, nil
}

// Hosts lists the inventory.
func (r *Reader) Hosts(ctx context.Context, f HostFilter) ([]HostSummary, error) {
	w, err := hostWhere(f)
	if err != nil {
		return nil, err
	}
	var out []HostSummary
	err = r.read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = hostsTx(ctx, tx, w)
		return err
	})
	return out, err
}

func hostsTx(ctx context.Context, tx *sql.Tx, w where) ([]HostSummary, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+summaryColumns+` FROM hosts h JOIN network_contexts c ON c.id = h.context_id`+
		w.sql()+` ORDER BY c.interface, h.mac`, w.args...)
	if err != nil {
		return nil, err
	}
	out := []HostSummary{}
	var ids []string
	for rows.Next() {
		h, err := scanSummary(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, h)
		ids = append(ids, h.HostID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ips, err := openIPs(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if v := ips[out[i].HostID]; v != nil {
			out[i].IPs = v
		}
	}
	return out, nil
}

// Host returns the full record of one host: every address and name binding
// (closed ones too), its TCP endpoints and DHCP leases.
func (r *Reader) Host(ctx context.Context, id string) (Host, error) {
	var h Host
	err := r.read(ctx, func(tx *sql.Tx) error {
		var err error
		h, err = r.hostRecord(ctx, tx, id, nil, true)
		return err
	})
	return h, err
}

// hostLeaseLimit bounds the lease rows a host record carries.
const hostLeaseLimit = 50

// hostRecord loads a host. With all, every binding; else the bindings in
// effect at at (or open now when at is nil).
func (r *Reader) hostRecord(ctx context.Context, tx *sql.Tx, id string, at *time.Time, all bool) (Host, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+summaryColumns+` FROM hosts h JOIN network_contexts c ON c.id = h.context_id
		WHERE h.host_id = ?`, id)
	s, err := scanSummary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, fmt.Errorf("host %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Host{}, err
	}
	h := Host{HostSummary: s, Addresses: []Binding{}, Names: []Name{}, Endpoints: []Endpoint{}, DHCPLeases: []DHCPLease{}}
	ips, err := openIPs(ctx, tx, []string{id})
	if err != nil {
		return Host{}, err
	}
	if v := ips[id]; v != nil {
		h.IPs = v
	}
	inEffect, args := `ended_at IS NULL`, []any{id}
	switch {
	case all:
		inEffect = `1`
	case at != nil:
		inEffect = `first_seen <= ? AND (ended_at IS NULL OR ended_at > ?)`
		args = append(args, msOf(*at), msOf(*at))
	}
	if h.Addresses, err = bindings(ctx, tx, `host_id = ? AND `+inEffect, args...); err != nil {
		return Host{}, err
	}
	if h.Names, err = r.names(ctx, tx, `n.host_id = ? AND `+inEffect, args...); err != nil {
		return Host{}, err
	}
	if h.Endpoints, err = r.endpoints(ctx, tx, `e.host_id = ?`, id); err != nil {
		return Host{}, err
	}
	leaseCond, leaseArgs := ` AND l.ended_at IS NULL`, []any{id, h.MAC}
	switch {
	case all:
		leaseCond = ``
	case at != nil:
		leaseCond = ` AND l.first_ack <= ? AND (l.ended_at IS NULL OR l.ended_at > ?)`
		leaseArgs = append(leaseArgs, msOf(*at), msOf(*at))
	}
	if h.DHCPLeases, err = leases(ctx, tx, ` WHERE l.context_id = (SELECT context_id FROM hosts WHERE host_id = ?) AND l.chaddr = ?`+leaseCond,
		hostLeaseLimit, leaseArgs...); err != nil {
		return Host{}, err
	}
	return h, nil
}

// bindings loads address bindings with their sources.
func bindings(ctx context.Context, tx *sql.Tx, cond string, args ...any) ([]Binding, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, ip, first_seen, last_seen, ended_at, conflict FROM addresses WHERE `+cond+
		` ORDER BY first_seen, id`, args...)
	if err != nil {
		return nil, err
	}
	var out []Binding
	var ids []int64
	for rows.Next() {
		var b Binding
		var id, first, last int64
		var ended sql.NullInt64
		var conflict int
		if err := rows.Scan(&id, &b.IP, &first, &last, &ended, &conflict); err != nil {
			rows.Close()
			return nil, err
		}
		b.FirstSeen, b.LastSeen, b.EndedAt, b.Conflict = timeOf(first), timeOf(last), timePtr(ended), conflict == 1
		out = append(out, b)
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, id := range ids {
		src, err := tx.QueryContext(ctx, `SELECT source, first_seen, last_seen FROM address_sources WHERE address_id = ? ORDER BY first_seen, source`, id)
		if err != nil {
			return nil, err
		}
		for src.Next() {
			var s SourceSeen
			var first, last int64
			if err := src.Scan(&s.Source, &first, &last); err != nil {
				src.Close()
				return nil, err
			}
			s.FirstSeen, s.LastSeen = timeOf(first), timeOf(last)
			out[i].Sources = append(out[i].Sources, s)
		}
		src.Close()
		if err := src.Err(); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = []Binding{}
	}
	return out, nil
}

// names loads name records (n) matching cond, oldest first.
func (r *Reader) names(ctx context.Context, tx *sql.Tx, cond string, args ...any) ([]Name, error) {
	rows, err := tx.QueryContext(ctx, `SELECT n.name, n.name_type, n.record_type, coalesce(n.address, ''), coalesce(n.value, ''), n.source,
		coalesce(n.advertiser_mac, ''), coalesce(n.advertiser_ip, ''), n.link, coalesce(n.host_id, ''), n.ttl_seconds, n.first_seen,
		n.last_seen, n.expires_at, n.ended_at, coalesce(n.end_reason, '') FROM name_records n WHERE `+cond+
		` ORDER BY n.first_seen, n.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Name{}
	now := r.now()
	for rows.Next() {
		var n Name
		var first, last int64
		var ttl, expires, ended sql.NullInt64
		if err := rows.Scan(&n.Name, &n.Type, &n.RecordType, &n.Address, &n.Value, &n.Source, &n.AdvertiserMAC, &n.AdvertiserIP,
			&n.Link, &n.HostID, &ttl, &first, &last, &expires, &ended, &n.EndReason); err != nil {
			return nil, err
		}
		n.FirstSeen, n.LastSeen, n.ExpiresAt, n.EndedAt = timeOf(first), timeOf(last), timePtr(expires), timePtr(ended)
		if ttl.Valid {
			n.TTL = &ttl.Int64
		}
		n.Freshness = r.freshness(n, now)
		out = append(out, n)
	}
	return out, rows.Err()
}

// freshness says, when read, whether a name record still stands.
func (r *Reader) freshness(n Name, now time.Time) string {
	switch {
	case n.EndedAt != nil:
		return "ended"
	case n.ExpiresAt != nil && !now.Before(*n.ExpiresAt):
		return "expired"
	case n.ExpiresAt == nil && r.nameExpiryD() > 0 && now.Sub(n.LastSeen) >= r.nameExpiryD():
		return "stale"
	}
	return "fresh"
}

// endpoints loads TCP endpoints (e) matching cond.
func (r *Reader) endpoints(ctx context.Context, tx *sql.Tx, cond string, args ...any) ([]Endpoint, error) {
	rows, err := tx.QueryContext(ctx, `SELECT c.interface, e.ip, e.proto, e.port, coalesce(e.host_id, ''), coalesce(h.mac, ''), e.last_result,
		e.last_result_at, coalesce(e.last_detail, ''), e.last_open_at, coalesce(e.last_definite_result, ''), e.last_definite_at, e.first_seen
		FROM endpoints e JOIN network_contexts c ON c.id = e.context_id LEFT JOIN hosts h ON h.host_id = e.host_id
		WHERE `+cond+` ORDER BY c.interface, e.port, e.ip`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Endpoint{}
	for rows.Next() {
		var e Endpoint
		var at, first int64
		var open, definite sql.NullInt64
		if err := rows.Scan(&e.Interface, &e.IP, &e.Proto, &e.Port, &e.HostID, &e.MAC, &e.LastResult, &at, &e.LastDetail, &open,
			&e.LastDefiniteResult, &definite, &first); err != nil {
			return nil, err
		}
		e.LastResultAt, e.LastOpenAt, e.LastDefiniteAt, e.FirstSeen = timeOf(at), timePtr(open), timePtr(definite), timeOf(first)
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Interface != b.Interface || a.Port != b.Port {
			return a.Interface < b.Interface || a.Interface == b.Interface && a.Port < b.Port
		}
		x, errX := netip.ParseAddr(a.IP)
		y, errY := netip.ParseAddr(b.IP)
		return errX == nil && errY == nil && x.Less(y)
	})
	return out, rows.Err()
}

type contextRow struct {
	id    int64
	iface string
}

func contexts(ctx context.Context, tx *sql.Tx, iface string) ([]contextRow, error) {
	var w where
	if iface != "" {
		w.add(`interface = ?`, iface)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, interface FROM network_contexts`+w.sql()+` ORDER BY interface`, w.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []contextRow
	for rows.Next() {
		var c contextRow
		if err := rows.Scan(&c.id, &c.iface); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Find answers `hosts find` (docs/CLI.md §4, docs/DATA_MODEL.md §4).
func (r *Reader) Find(ctx context.Context, q FindQuery) (FindResult, error) {
	kind, norm, ok := Normalize(q.Kind, q.Query)
	if !ok {
		return FindResult{}, fmt.Errorf("%w: %q is not a %s", ErrBadQuery, q.Query, q.Kind)
	}
	if q.At != nil {
		at := q.At.UTC()
		q.At = &at
	}
	res := FindResult{Kind: kind, Query: norm, At: q.At, Hosts: []FoundHost{}}
	err := r.read(ctx, func(tx *sql.Tx) error {
		if kind == KindIP {
			return r.findIP(ctx, tx, &res, q.Interface)
		}
		ids, err := matchingHosts(ctx, tx, kind, norm, q.Interface, q.At)
		if err != nil {
			return err
		}
		for _, id := range ids {
			h, err := r.hostRecord(ctx, tx, id, q.At, false)
			if err != nil {
				return err
			}
			res.Hosts = append(res.Hosts, FoundHost{Host: h})
		}
		if kind == KindName {
			// What was advertised or resolved for the name without one MAC
			// holding the address: a name record, not a host.
			cond, args := `n.host_id IS NULL AND n.name = ? COLLATE NOCASE AND n.ended_at IS NULL`, []any{norm}
			if q.At != nil {
				cond, args = `n.host_id IS NULL AND n.name = ? COLLATE NOCASE AND n.first_seen <= ? AND (n.ended_at IS NULL OR n.ended_at > ?)`,
					[]any{norm, msOf(*q.At), msOf(*q.At)}
			}
			if q.Interface != "" {
				cond += ` AND n.context_id = (SELECT id FROM network_contexts WHERE interface = ?)`
				args = append(args, q.Interface)
			}
			adv, err := r.names(ctx, tx, cond, args...)
			if err != nil {
				return err
			}
			if len(adv) > 0 {
				res.Advertised = adv
			}
		}
		return nil
	})
	return res, err
}

// matchingHosts returns the hosts a MAC, name or host query matches now
// (open name) or at a time.
func matchingHosts(ctx context.Context, tx *sql.Tx, kind QueryKind, q, iface string, at *time.Time) ([]string, error) {
	var w where
	if iface != "" {
		w.add(`c.interface = ?`, iface)
	}
	switch kind {
	case KindName:
		inEffect, args := `n.ended_at IS NULL`, []any{q}
		if at != nil {
			inEffect = `n.first_seen <= ? AND (n.ended_at IS NULL OR n.ended_at > ?)`
			args = append(args, msOf(*at), msOf(*at))
		}
		w.add(`h.host_id IN (SELECT n.host_id FROM name_records n WHERE n.name = ? COLLATE NOCASE AND `+inEffect+`)`, args...)
	default:
		matchCondition(&w, kind, q)
		if at != nil {
			w.add(`h.first_seen <= ?`, msOf(*at))
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT h.host_id FROM hosts h JOIN network_contexts c ON c.id = h.context_id`+
		w.sql()+` ORDER BY c.interface, h.mac`, w.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// holding is a binding row with its holder.
type holding struct {
	Holding
	id int64
}

func holdings(ctx context.Context, tx *sql.Tx, cond string, args ...any) ([]holding, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.id, c.interface, a.host_id, h.mac, a.ip, a.first_seen, a.last_seen, a.ended_at, a.conflict
		FROM addresses a JOIN network_contexts c ON c.id = a.context_id JOIN hosts h ON h.host_id = a.host_id WHERE `+cond, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []holding
	for rows.Next() {
		var x holding
		var first, last int64
		var ended sql.NullInt64
		var conflict int
		if err := rows.Scan(&x.id, &x.Interface, &x.HostID, &x.MAC, &x.Binding.IP, &first, &last, &ended, &conflict); err != nil {
			return nil, err
		}
		x.Binding.FirstSeen, x.Binding.LastSeen, x.Binding.EndedAt, x.Binding.Conflict = timeOf(first), timeOf(last), timePtr(ended), conflict == 1
		out = append(out, x)
	}
	return out, rows.Err()
}

func firstHolding(ctx context.Context, tx *sql.Tx, cond string, args ...any) (*Holding, error) {
	hs, err := holdings(ctx, tx, cond+` LIMIT 1`, args...)
	if err != nil || len(hs) == 0 {
		return nil, err
	}
	return &hs[0].Holding, nil
}

// findIP answers an address query: the open bindings, or the point-in-time
// holders with what replaced them, per interface.
func (r *Reader) findIP(ctx context.Context, tx *sql.Tx, res *FindResult, iface string) error {
	ctxs, err := contexts(ctx, tx, iface)
	if err != nil {
		return err
	}
	ip := res.Query
	for _, c := range ctxs {
		var hs []holding
		if res.At == nil {
			hs, err = holdings(ctx, tx, `a.context_id = ? AND a.ip = ? AND a.ended_at IS NULL ORDER BY a.last_seen DESC`, c.id, ip)
		} else {
			t := msOf(*res.At)
			hs, err = holdings(ctx, tx, `a.context_id = ? AND a.ip = ? AND a.first_seen <= ? AND (a.ended_at IS NULL OR a.ended_at > ?)
				ORDER BY a.last_seen DESC`, c.id, ip, t, t)
		}
		if err != nil {
			return err
		}
		if len(hs) == 0 {
			if res.At != nil {
				if err := neighbours(ctx, tx, res, c, ip); err != nil {
					return err
				}
			}
			continue
		}
		for _, x := range hs {
			fh, err := r.foundHolder(ctx, tx, res, c, x, len(hs) >= 2)
			if err != nil {
				return err
			}
			res.Hosts = append(res.Hosts, fh)
		}
	}
	return nil
}

func (r *Reader) foundHolder(ctx context.Context, tx *sql.Tx, res *FindResult, c contextRow, x holding, conflict bool) (FoundHost, error) {
	h, err := r.hostRecord(ctx, tx, x.HostID, res.At, false)
	if err != nil {
		return FoundHost{}, err
	}
	b := x.Binding
	if bs, err := bindings(ctx, tx, `id = ?`, x.id); err == nil && len(bs) == 1 {
		b = bs[0]
	} else if err != nil {
		return FoundHost{}, err
	}
	fh := FoundHost{Host: h, Binding: &b, Conflict: b.Conflict}
	if res.At == nil {
		return fh, nil
	}
	t := msOf(*res.At)
	fh.Conflict = conflict
	fh.Unconfirmed = res.At.After(b.LastSeen)
	// What replaced the binding: the first binding of the address by another
	// host that started when or after it ended (a claimant during the binding
	// is a conflict, not a successor), and the host's next new address.
	if b.EndedAt != nil {
		end := msOf(*b.EndedAt)
		if fh.ReplacedBy, err = firstHolding(ctx, tx, `a.context_id = ? AND a.ip = ? AND a.host_id != ? AND a.first_seen >= ?
			ORDER BY a.first_seen, a.id`, c.id, res.Query, x.HostID, end); err != nil {
			return FoundHost{}, err
		}
		if fh.NextAddress, err = firstHolding(ctx, tx, `a.host_id = ? AND a.ip != ? AND a.first_seen >= ? ORDER BY a.first_seen, a.id`,
			x.HostID, res.Query, end); err != nil {
			return FoundHost{}, err
		}
	}
	if conflict {
		evs, err := queryEvents(ctx, tx, ` WHERE e.type = 'DUPLICATE_IP_DETECTED' AND e.context_id = ? AND e.new_value = ? AND e.ts <= ?
			ORDER BY e.ts DESC, e.id DESC LIMIT 1`, c.id, res.Query, t)
		if err != nil {
			return FoundHost{}, err
		}
		if len(evs) == 1 {
			fh.ConflictEvent = &evs[0]
		}
	}
	return fh, nil
}

// neighbours adds the bindings before and after At for an address with no
// holder at At (none if the address was never bound on that interface).
func neighbours(ctx context.Context, tx *sql.Tx, res *FindResult, c contextRow, ip string) error {
	t := msOf(*res.At)
	prev, err := firstHolding(ctx, tx, `a.context_id = ? AND a.ip = ? AND a.ended_at <= ? ORDER BY a.ended_at DESC, a.id DESC`, c.id, ip, t)
	if err != nil {
		return err
	}
	next, err := firstHolding(ctx, tx, `a.context_id = ? AND a.ip = ? AND a.first_seen > ? ORDER BY a.first_seen, a.id`, c.id, ip, t)
	if err != nil {
		return err
	}
	if prev != nil {
		res.Previous = append(res.Previous, *prev)
	}
	if next != nil {
		res.Next = append(res.Next, *next)
	}
	return nil
}

const eventColumns = `e.id, e.ts, e.type, e.severity, coalesce(c.interface, ''), coalesce(e.host_id, ''), coalesce(h.mac, ''),
	coalesce(e.related_host_id, ''), coalesce(rh.mac, ''), coalesce(e.old_value, ''), coalesce(e.new_value, ''), e.cause,
	coalesce(e.observation_id, 0), e.evidence_json, e.clock_sync`

const eventJoins = ` FROM events e LEFT JOIN network_contexts c ON c.id = e.context_id
	LEFT JOIN hosts h ON h.host_id = e.host_id LEFT JOIN hosts rh ON rh.host_id = e.related_host_id`

// queryEvents runs an event query; tail is the WHERE clause (if any) and
// the ordering.
func queryEvents(ctx context.Context, tx *sql.Tx, tail string, args ...any) ([]Event, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+eventColumns+eventJoins+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var ts int64
		var ev string
		if err := rows.Scan(&e.ID, &ts, &e.Type, &e.Severity, &e.Interface, &e.HostID, &e.MAC, &e.RelatedHostID, &e.RelatedMAC,
			&e.Old, &e.New, &e.Cause, &e.ObservationID, &ev, &e.ClockSync); err != nil {
			return nil, err
		}
		e.TS, e.Evidence = timeOf(ts), []byte(ev)
		out = append(out, e)
	}
	return out, rows.Err()
}

func timeRange(w *where, col string, since, until time.Time) {
	if !since.IsZero() {
		w.add(col+` >= ?`, msOf(since))
	}
	if !until.IsZero() {
		w.add(col+` <= ?`, msOf(until))
	}
}

// History returns a timeline in time order (docs/CLI.md §4): for a host or
// MAC every event of the host (or of each host with that MAC); for an IP
// every event whose old value, new value or evidence IP is that IP; for a
// name every event of the hosts that ever held it.
func (r *Reader) History(ctx context.Context, q HistoryQuery) ([]Event, error) {
	kind, norm, ok := Normalize(q.Kind, q.Query)
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a %s", ErrBadQuery, q.Query, q.Kind)
	}
	var w where
	if kind == KindIP {
		w.add(`(e.old_value = ? OR e.new_value = ? OR json_extract(e.evidence_json, '$.ip') = ?)`, norm, norm, norm)
		if q.Interface != "" {
			w.add(`c.interface = ?`, q.Interface)
		}
	} else {
		var hw where
		if q.Interface != "" {
			hw.add(`c.interface = ?`, q.Interface)
		}
		matchCondition(&hw, kind, norm)
		sub := `SELECT h.host_id FROM hosts h JOIN network_contexts c ON c.id = h.context_id` + hw.sql()
		w.add(`(e.host_id IN (`+sub+`) OR e.related_host_id IN (`+sub+`))`, append(hw.args, hw.args...)...)
	}
	timeRange(&w, "e.ts", q.Since, q.Until)
	var out []Event
	err := r.read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = queryEvents(ctx, tx, w.sql()+` ORDER BY e.ts, e.id`, w.args...)
		return err
	})
	return out, err
}

// normalizeFilter returns a MAC or IP filter value in stored form; empty
// stays empty.
func normalizeFilter(kind QueryKind, v string) (string, error) {
	if v == "" {
		return "", nil
	}
	k, norm := Classify(v)
	if k != kind {
		return "", fmt.Errorf("%w: %q is not a %s", ErrBadQuery, v, kind)
	}
	return norm, nil
}

// DefaultEventLimit bounds an events query without a limit, so a request
// never loads two years of events at once.
const DefaultEventLimit = 10000

// Events lists the latest events matching f (at most f.Limit, default
// DefaultEventLimit), in time order.
func (r *Reader) Events(ctx context.Context, f EventFilter) ([]Event, error) {
	var err error
	if f.MAC, err = normalizeFilter(KindMAC, f.MAC); err != nil {
		return nil, err
	}
	if f.IP, err = normalizeFilter(KindIP, f.IP); err != nil {
		return nil, err
	}
	if f.Limit <= 0 {
		f.Limit = DefaultEventLimit
	}
	var w where
	timeRange(&w, "e.ts", f.Since, f.Until)
	if len(f.Types) > 0 {
		w.add(`e.type IN (`+placeholders(len(f.Types))+`)`, anys(f.Types)...)
	}
	if f.Interface != "" {
		w.add(`c.interface = ?`, f.Interface)
	}
	if f.MAC != "" {
		w.add(`(e.host_id IN (SELECT host_id FROM hosts WHERE mac = ?) OR e.related_host_id IN (SELECT host_id FROM hosts WHERE mac = ?))`, f.MAC, f.MAC)
	}
	if f.IP != "" {
		w.add(`(e.old_value = ? OR e.new_value = ? OR json_extract(e.evidence_json, '$.ip') = ?)`, f.IP, f.IP, f.IP)
	}
	if f.HostID != "" {
		w.add(`(e.host_id = ? OR e.related_host_id = ?)`, f.HostID, f.HostID)
	}
	order := fmt.Sprintf(` ORDER BY e.ts DESC, e.id DESC LIMIT %d`, f.Limit)
	var out []Event
	err = r.read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = queryEvents(ctx, tx, w.sql()+order, w.args...)
		return err
	})
	reverse(out)
	return out, err
}

func reverse[T any](s []T) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// DefaultObservationLimit is the default `observations list --limit`.
const DefaultObservationLimit = 1000

// normalize puts the MAC and IP filters in stored form.
func (f *ObservationFilter) normalize() error {
	var err error
	if f.MAC, err = normalizeFilter(KindMAC, f.MAC); err != nil {
		return err
	}
	f.IP, err = normalizeFilter(KindIP, f.IP)
	return err
}

// Observations lists the latest raw observations matching f, in time order.
// Filters other than the host and time walk the time index back from the
// latest observation until the limit is filled.
func (r *Reader) Observations(ctx context.Context, f ObservationFilter) ([]Observation, error) {
	if err := f.normalize(); err != nil {
		return nil, err
	}
	w := observationWhere(f, "o.ts")
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultObservationLimit
	}
	out := []Observation{}
	err := r.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT o.id, o.ts, c.interface, o.source, coalesce(o.mac, ''), coalesce(o.ip, ''),
			coalesce(o.hostname, ''), coalesce(o.name_type, ''), coalesce(o.service_json, ''), coalesce(o.meta_json, ''), coalesce(o.host_id, '')
			FROM observations o JOIN network_contexts c ON c.id = o.context_id`+w.sql()+
			fmt.Sprintf(` ORDER BY o.ts DESC, o.id DESC LIMIT %d`, limit), w.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o Observation
			var ts int64
			var svc, meta string
			if err := rows.Scan(&o.ID, &ts, &o.Interface, &o.Source, &o.MAC, &o.IP, &o.Hostname, &o.NameType, &svc, &meta, &o.HostID); err != nil {
				return err
			}
			o.TS = timeOf(ts)
			if svc != "" {
				o.Service = []byte(svc)
			}
			if meta != "" {
				o.Meta = []byte(meta)
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	reverse(out)
	return out, err
}

func observationWhere(f ObservationFilter, tsCol string) where {
	var w where
	timeRange(&w, tsCol, f.Since, f.Until)
	alias := strings.Split(tsCol, ".")[0]
	if f.HostID != "" {
		w.add(alias+`.host_id = ?`, f.HostID)
	}
	if f.Unbound {
		w.add(alias + `.host_id IS NULL`)
	}
	if f.Interface != "" {
		w.add(`c.interface = ?`, f.Interface)
	}
	if f.MAC != "" {
		w.add(alias+`.mac = ?`, f.MAC)
	}
	if f.IP != "" {
		w.add(alias+`.ip = ?`, f.IP)
	}
	if f.Source != "" {
		w.add(alias+`.source = ?`, f.Source)
	}
	return w
}

// Rollups lists the latest hourly roll-ups matching f (Since and Until
// apply to the hour), in time order.
func (r *Reader) Rollups(ctx context.Context, f ObservationFilter) ([]Rollup, error) {
	if err := f.normalize(); err != nil {
		return nil, err
	}
	w := observationWhere(f, "o.hour")
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultObservationLimit
	}
	out := []Rollup{}
	err := r.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT o.hour, c.interface, coalesce(o.host_id, ''), o.source, coalesce(o.mac, ''), coalesce(o.ip, ''),
			o.count, o.first_ts, o.last_ts FROM observation_rollups o JOIN network_contexts c ON c.id = o.context_id`+w.sql()+
			fmt.Sprintf(` ORDER BY o.hour DESC, o.id DESC LIMIT %d`, limit), w.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x Rollup
			var hour, first, last int64
			if err := rows.Scan(&hour, &x.Interface, &x.HostID, &x.Source, &x.MAC, &x.IP, &x.Count, &first, &last); err != nil {
				return err
			}
			x.Hour, x.FirstTS, x.LastTS = timeOf(hour), timeOf(first), timeOf(last)
			out = append(out, x)
		}
		return rows.Err()
	})
	reverse(out)
	return out, err
}

// Services lists the TCP endpoints: per address and port, the last
// result, the last definite answer and the MAC it was attributed to. With
// At, the result in effect then, from the endpoint's history.
func (r *Reader) Services(ctx context.Context, f ServiceFilter) ([]Endpoint, error) {
	var w where
	if f.Port > 0 {
		w.add(`e.port = ?`, f.Port)
	}
	if f.Interface != "" {
		w.add(`c.interface = ?`, f.Interface)
	}
	if f.IP != "" {
		ip, err := netip.ParseAddr(f.IP)
		if err != nil {
			return nil, fmt.Errorf("%w: %q is not an IP address", ErrBadQuery, f.IP)
		}
		w.add(`e.ip = ?`, ip.Unmap().String())
	}
	if f.At != nil {
		w.add(`e.first_seen <= ?`, msOf(*f.At))
	}
	var out []Endpoint
	err := r.read(ctx, func(tx *sql.Tx) error {
		cond := "1"
		if len(w.conds) > 0 {
			cond = strings.Join(w.conds, " AND ")
		}
		eps, err := r.endpoints(ctx, tx, cond, w.args...)
		if err != nil {
			return err
		}
		if f.At != nil {
			if eps, err = endpointsAt(ctx, tx, eps, *f.At); err != nil {
				return err
			}
		}
		for _, e := range eps {
			if f.State == "" || strings.EqualFold(e.LastResult, f.State) {
				out = append(out, e)
			}
		}
		return nil
	})
	if out == nil {
		out = []Endpoint{}
	}
	return out, err
}

// endpointsAt replaces each endpoint's last result with the one in effect
// at t: its latest change at or before t, and the latest definite answer
// by then. An endpoint with no recorded result by t (tested later, or its
// history pruned) is left out: its result at t is unknown, and the current
// one would be a guess.
func endpointsAt(ctx context.Context, tx *sql.Tx, eps []Endpoint, t time.Time) ([]Endpoint, error) {
	var out []Endpoint
	for i := range eps {
		e := &eps[i]
		found := false
		rows, err := tx.QueryContext(ctx, `SELECT r.result, r.ts, coalesce(r.host_id, ''), coalesce(r.detail, ''), coalesce(h.mac, '')
			FROM endpoint_results r JOIN endpoints e ON e.id = r.endpoint_id JOIN network_contexts c ON c.id = e.context_id
			LEFT JOIN hosts h ON h.host_id = r.host_id
			WHERE c.interface = ? AND e.ip = ? AND e.proto = ? AND e.port = ? AND r.ts <= ? ORDER BY r.ts, r.id`,
			e.Interface, e.IP, e.Proto, e.Port, msOf(t))
		if err != nil {
			return nil, err
		}
		e.LastOpenAt, e.LastDefiniteAt, e.LastDefiniteResult = nil, nil, ""
		for rows.Next() {
			var ts int64
			if err := rows.Scan(&e.LastResult, &ts, &e.HostID, &e.LastDetail, &e.MAC); err != nil {
				rows.Close()
				return nil, err
			}
			at := timeOf(ts)
			e.LastResultAt, found = at, true
			if e.LastResult == "OPEN" || e.LastResult == "REFUSED" {
				e.LastDefiniteResult, e.LastDefiniteAt = e.LastResult, &at
			}
			if e.LastResult == "OPEN" {
				e.LastOpenAt = &at
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if found {
			out = append(out, *e)
		}
	}
	return out, nil
}

// Interfaces lists the network contexts with their open prefixes, host
// counts and the state of their last INTERFACE_UP/DOWN event ("unknown"
// before any). The daemon replaces state and MAC with live values.
func (r *Reader) Interfaces(ctx context.Context) ([]InterfaceInfo, error) {
	out := []InterfaceInfo{}
	err := r.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT c.id, c.interface, c.first_seen, c.last_seen,
			(SELECT count(*) FROM hosts h WHERE h.context_id = c.id),
			coalesce((SELECT type FROM (
				SELECT * FROM (SELECT type, ts, id FROM events WHERE context_id = c.id AND type = 'INTERFACE_UP' ORDER BY ts DESC, id DESC LIMIT 1)
				UNION ALL
				SELECT * FROM (SELECT type, ts, id FROM events WHERE context_id = c.id AND type = 'INTERFACE_DOWN' ORDER BY ts DESC, id DESC LIMIT 1)
			) ORDER BY ts DESC, id DESC LIMIT 1), '')
			FROM network_contexts c ORDER BY c.interface`)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var i InterfaceInfo
			var id, first, last int64
			var ev string
			if err := rows.Scan(&id, &i.Name, &first, &last, &i.Hosts, &ev); err != nil {
				rows.Close()
				return err
			}
			i.FirstSeen, i.LastSeen, i.Prefixes = timeOf(first), timeOf(last), []string{}
			switch ev {
			case "INTERFACE_UP":
				i.State = "up"
			case "INTERFACE_DOWN":
				i.State = "down"
			default:
				i.State = "unknown"
			}
			out = append(out, i)
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for k, id := range ids {
			ps, err := tx.QueryContext(ctx, `SELECT prefix FROM context_prefixes WHERE context_id = ? AND ended_at IS NULL ORDER BY prefix`, id)
			if err != nil {
				return err
			}
			for ps.Next() {
				var p string
				if err := ps.Scan(&p); err != nil {
					ps.Close()
					return err
				}
				out[k].Prefixes = append(out[k].Prefixes, p)
			}
			ps.Close()
			if err := ps.Err(); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// countedTables are the tables `db info` counts.
var countedTables = []string{
	"network_contexts", "context_prefixes", "hosts", "addresses", "address_sources", "observations", "observation_rollups",
	"events", "scans", "runtime_state", "endpoints", "endpoint_results", "name_records", "dhcp_servers", "dhcp_exchanges", "dhcp_leases",
}

// DBInfo describes the database file and counts the rows of every table.
func (r *Reader) DBInfo(ctx context.Context) (DBInfo, error) {
	return r.dbInfo(ctx, true)
}

// DBSummary is DBInfo without the row counts, which scan whole tables; it
// is cheap enough for every status request and metrics scrape.
func (r *Reader) DBSummary(ctx context.Context) (DBInfo, error) {
	return r.dbInfo(ctx, false)
}

func (r *Reader) dbInfo(ctx context.Context, rows bool) (DBInfo, error) {
	info := DBInfo{Path: r.path}
	if fi, err := os.Stat(r.path); err == nil {
		info.Size = fi.Size()
	}
	if fi, err := os.Stat(r.path + "-wal"); err == nil {
		info.WALSize = fi.Size()
	}
	err := r.read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&info.JournalMode); err != nil {
			return err
		}
		if r.Immutable && walHeader(r.path) {
			// An immutable open reports "delete"; the file header says
			// the database is in WAL mode.
			info.JournalMode = "wal"
		}
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&info.SchemaVersion); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT (page_count - freelist_count) * page_size
			FROM pragma_page_count(), pragma_freelist_count(), pragma_page_size()`).Scan(&info.UsedSize); err != nil {
			return err
		}
		if !rows {
			return nil
		}
		info.Rows = map[string]int64{}
		for _, t := range countedTables {
			var n int64
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+t).Scan(&n); err != nil {
				return err
			}
			info.Rows[t] = n
		}
		return nil
	})
	return info, err
}

// Check runs PRAGMA integrity_check (`db check`).
func (r *Reader) Check(ctx context.Context) (CheckResult, error) {
	var res CheckResult
	err := r.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `PRAGMA integrity_check`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			if s != "ok" {
				res.Problems = append(res.Problems, s)
			}
		}
		return rows.Err()
	})
	res.OK = err == nil && len(res.Problems) == 0
	return res, err
}

// Evidence answers `hosts evidence` for one host.
func (r *Reader) Evidence(ctx context.Context, id string, since time.Time) (Evidence, error) {
	var ev Evidence
	err := r.read(ctx, func(tx *sql.Tx) error {
		var err error
		ev, err = r.evidenceTx(ctx, tx, id, since)
		return err
	})
	return ev, err
}

// EvidenceFor answers `hosts evidence` for every host that ever matched the
// filter, in one read transaction (one snapshot).
func (r *Reader) EvidenceFor(ctx context.Context, f HostFilter, since time.Time) ([]Evidence, error) {
	w, err := hostWhere(f)
	if err != nil {
		return nil, err
	}
	out := []Evidence{}
	err = r.read(ctx, func(tx *sql.Tx) error {
		hosts, err := hostsTx(ctx, tx, w)
		if err != nil {
			return err
		}
		for _, h := range hosts {
			ev, err := r.evidenceTx(ctx, tx, h.HostID, since)
			if err != nil {
				return err
			}
			out = append(out, ev)
		}
		return nil
	})
	return out, err
}

func (r *Reader) evidenceTx(ctx context.Context, tx *sql.Tx, id string, since time.Time) (Evidence, error) {
	ev := Evidence{Counts: []SourceCount{}}
	var err error
	if ev.Host, err = r.hostRecord(ctx, tx, id, nil, true); err != nil {
		return ev, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT source, ip, sum(n), min(first), max(last) FROM (
			SELECT source, coalesce(ip, '') AS ip, count(*) AS n, min(ts) AS first, max(ts) AS last
			FROM observations WHERE host_id = ? AND ts >= ? GROUP BY source, ip
			UNION ALL
			SELECT source, coalesce(ip, ''), sum(count), min(first_ts), max(last_ts)
			FROM observation_rollups WHERE host_id = ? AND last_ts >= ? GROUP BY source, ip
		) GROUP BY source, ip ORDER BY source, ip`, id, msOf(since), id, msOf(since))
	if err != nil {
		return ev, err
	}
	for rows.Next() {
		var c SourceCount
		var first, last int64
		if err := rows.Scan(&c.Source, &c.IP, &c.Count, &first, &last); err != nil {
			rows.Close()
			return ev, err
		}
		c.FirstSeen, c.LastSeen = timeOf(first), timeOf(last)
		ev.Counts = append(ev.Counts, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ev, err
	}
	var w where
	w.add(`(e.host_id = ? OR e.related_host_id = ?)`, id, id)
	timeRange(&w, "e.ts", since, time.Time{})
	ev.Events, err = queryEvents(ctx, tx, w.sql()+` ORDER BY e.ts, e.id`, w.args...)
	return ev, err
}

// ActiveState is the active-discovery kill switch (docs/DATA_MODEL.md §8):
// the persisted runtime_state row, and whether LAN_TRACKER_ACTIVE_DISABLED
// forces it regardless (Forced, set by the caller).
type ActiveState struct {
	Disabled bool       `json:"disabled"`
	Reason   string     `json:"reason,omitempty"`
	By       string     `json:"by,omitempty"`
	At       *time.Time `json:"at,omitempty"`
	Forced   bool       `json:"forced,omitempty"`
}

// ActiveState reads the persisted kill switch; no row means enabled.
func (r *Reader) ActiveState(ctx context.Context) (ActiveState, error) {
	var a ActiveState
	err := r.read(ctx, func(tx *sql.Tx) error {
		var v string
		err := tx.QueryRowContext(ctx, `SELECT value FROM runtime_state WHERE key = 'active_disabled'`).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(v), &a)
	})
	return a, err
}

// Scan is an operator scan on one interface (a scans row).
type Scan struct {
	ID        int64           `json:"id"`
	Interface string          `json:"interface"`
	Kind      string          `json:"kind"`
	Trigger   string          `json:"trigger"`
	Started   time.Time       `json:"started"`
	Finished  *time.Time      `json:"finished,omitempty"`
	Targets   int             `json:"targets"`
	Results   json.RawMessage `json:"results"`
}

// LastScan returns the latest scan, or nil if there was none.
func (r *Reader) LastScan(ctx context.Context) (*Scan, error) {
	var out *Scan
	err := r.read(ctx, func(tx *sql.Tx) error {
		var s Scan
		var started int64
		var finished sql.NullInt64
		var results string
		err := tx.QueryRowContext(ctx, `SELECT s.id, c.interface, s.kind, s.trigger, s.started_at, s.finished_at, s.targets, s.results_json
			FROM scans s JOIN network_contexts c ON c.id = s.context_id ORDER BY s.started_at DESC, s.id DESC LIMIT 1`).
			Scan(&s.ID, &s.Interface, &s.Kind, &s.Trigger, &started, &finished, &s.Targets, &results)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		s.Started, s.Results = timeOf(started), json.RawMessage(results)
		if finished.Valid {
			t := timeOf(finished.Int64)
			s.Finished = &t
		}
		out = &s
		return nil
	})
	return out, err
}

// KnownAddr is an address active probes may target: an open IPv4
// binding, and when it was last confirmed (presence evidence).
type KnownAddr struct {
	IP       netip.Addr
	LastSeen time.Time
}

// KnownIPv4 returns the open IPv4 bindings of an interface.
func (r *Reader) KnownIPv4(ctx context.Context, iface string) ([]KnownAddr, error) {
	var out []KnownAddr
	err := r.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT a.ip, max(a.last_seen) FROM addresses a JOIN network_contexts c ON c.id = a.context_id
			WHERE c.interface = ? AND a.family = 4 AND a.ended_at IS NULL GROUP BY a.ip ORDER BY a.ip`, iface)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			var last int64
			if err := rows.Scan(&s, &last); err != nil {
				return err
			}
			if ip, err := netip.ParseAddr(s); err == nil {
				out = append(out, KnownAddr{IP: ip, LastSeen: timeOf(last)})
			}
		}
		return rows.Err()
	})
	return out, err
}

// NameTargets are what one pass of name queries on an interface may ask
// about (FR-AC-12): names and addresses already known, never more. Each
// list leaves out what a record's TTL still covers and is stalest first.
type NameTargets struct {
	MDNS []string     // .local names of the context's mDNS address records
	DNS  []string     // names of its DNS address and CNAME records
	PTR  []netip.Addr // open IPv4 bindings without a PTR record in effect
}

// NameTargets lists up to limit targets of each kind on iface at now.
func (r *Reader) NameTargets(ctx context.Context, iface string, now time.Time, limit int) (NameTargets, error) {
	var out NameTargets
	t := msOf(now)
	names := func(tx *sql.Tx, cond string) ([]string, error) {
		rows, err := tx.QueryContext(ctx, `SELECT min(n.name) FROM name_records n JOIN network_contexts c ON c.id = n.context_id
			WHERE c.interface = ? AND n.ended_at IS NULL AND `+cond+`
			GROUP BY lower(n.name) HAVING max(coalesce(n.expires_at, 0)) <= ? ORDER BY max(n.last_seen), lower(n.name) LIMIT ?`, iface, t, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, rows.Err()
	}
	err := r.read(ctx, func(tx *sql.Tx) error {
		var err error
		if out.MDNS, err = names(tx, `n.name_type = 'mdns' AND n.record_type IN ('A', 'AAAA') AND lower(n.name) LIKE '%.local'`); err != nil {
			return err
		}
		if out.DNS, err = names(tx, `n.name_type = 'dns' AND n.record_type IN ('A', 'AAAA', 'CNAME') AND lower(n.name) NOT LIKE '%.local'`); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT a.ip FROM addresses a JOIN network_contexts c ON c.id = a.context_id
			WHERE c.interface = ? AND a.family = 4 AND a.ended_at IS NULL AND NOT EXISTS (
				SELECT 1 FROM name_records n WHERE n.context_id = a.context_id AND n.record_type = 'PTR' AND n.address = a.ip
					AND n.ended_at IS NULL AND n.expires_at > ?)
			GROUP BY a.ip ORDER BY (SELECT max(n.last_seen) FROM name_records n WHERE n.context_id = a.context_id
				AND n.record_type = 'PTR' AND n.address = a.ip) NULLS FIRST, a.ip LIMIT ?`, iface, t, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			if ip, err := netip.ParseAddr(s); err == nil {
				out.PTR = append(out.PTR, ip)
			}
		}
		return rows.Err()
	})
	return out, err
}

// DHCPServers lists the DHCP servers seen, by interface and identity.
func (r *Reader) DHCPServers(ctx context.Context, f DHCPServerFilter) ([]DHCPServer, error) {
	var w where
	if f.Interface != "" {
		w.add(`c.interface = ?`, f.Interface)
	}
	if f.Status != "" {
		w.add(`d.status = ?`, strings.ToLower(f.Status))
	}
	out := []DHCPServer{}
	err := r.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT c.interface, d.server_id, d.relay, d.mac, d.ip, coalesce(d.host_id, ''), d.status,
			d.config_json, d.first_seen, d.last_seen
			FROM dhcp_servers d JOIN network_contexts c ON c.id = d.context_id`+w.sql()+
			` ORDER BY c.interface, d.server_id = '', d.server_id, d.relay, d.last_seen DESC, d.id`, w.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d DHCPServer
			var cfg string
			var first, last int64
			if err := rows.Scan(&d.Interface, &d.ServerID, &d.Relay, &d.MAC, &d.IP, &d.HostID, &d.Status, &cfg, &first, &last); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(cfg), &d.Config); err != nil || d.Config == nil {
				d.Config = map[string]string{}
			}
			d.FirstSeen, d.LastSeen = timeOf(first), timeOf(last)
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// DHCPServerCounts counts, per interface and status, the DHCP servers
// (identity, relay and sender) heard from since the given time.
func (r *Reader) DHCPServerCounts(ctx context.Context, since time.Time) (map[string]map[string]int, error) {
	out := map[string]map[string]int{}
	err := r.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT c.interface, d.status, count(*) FROM dhcp_servers d
			JOIN network_contexts c ON c.id = d.context_id WHERE d.last_seen >= ? GROUP BY c.interface, d.status`, since.UnixMilli())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var iface, status string
			var n int
			if err := rows.Scan(&iface, &status, &n); err != nil {
				return err
			}
			if out[iface] == nil {
				out[iface] = map[string]int{}
			}
			out[iface][status] = n
		}
		return rows.Err()
	})
	return out, err
}

// HostCounts counts hosts per interface and presence.
func (r *Reader) HostCounts(ctx context.Context) (map[string]map[string]int, error) {
	out := map[string]map[string]int{}
	err := r.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT c.interface, h.presence, count(*) FROM hosts h
			JOIN network_contexts c ON c.id = h.context_id GROUP BY c.interface, h.presence`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var iface, presence string
			var n int
			if err := rows.Scan(&iface, &presence, &n); err != nil {
				return err
			}
			if out[iface] == nil {
				out[iface] = map[string]int{}
			}
			out[iface][presence] = n
		}
		return rows.Err()
	})
	return out, err
}

// walHeader reports whether the database file header marks WAL mode (read
// and write format versions 2, bytes 18 and 19).
func walHeader(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var h [20]byte
	if _, err := f.ReadAt(h[:], 0); err != nil {
		return false
	}
	return h[18] == 2 && h[19] == 2
}
