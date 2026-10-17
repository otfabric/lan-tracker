package correlate

import (
	"context"
	"database/sql"
	"net/netip"
	"time"

	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/observation"
)

// endpointKey is one TCP endpoint: an address and port on a context
// (docs/DATA_MODEL.md §5.5). The MAC holding the address is an attribute
// of each result, never part of the key, so two addresses of one MAC keep
// their own results.
type endpointKey struct {
	ctx   int64
	ip    netip.Addr
	proto string
	port  int
}

type endpoint struct {
	id                    int64
	key                   endpointKey
	hostID                string // the sole holder at the last result; "" when none or several
	result                observation.ServiceState
	resultAt              time.Time
	detail                string
	openAt                time.Time
	definite              observation.ServiceState // the last OPEN or REFUSED
	definiteAt, firstSeen time.Time
}

// endpointResult applies §5.5 to a TCP result for ip on context n. holder
// is the host that alone held ip when the result came in, nil when none
// or several did: the result is kept for the endpoint either way and is
// attributed to no MAC unless one held the address alone. The last result
// and the last definite answer are kept apart, so a timeout never erases
// that the endpoint last answered OPEN. Every change of the result or of
// the attributed MAC is a row of endpoint_results; SERVICE_STATE_CHANGED
// marks the endpoint opening or leaving OPEN, with the exact old and new
// results. Repeated identical results change nothing but the time.
func (c *Correlator) endpointResult(ctx context.Context, n *netContext, ip netip.Addr, holder *host, o observation.Observation, obsID int64, ev events.Evidence) {
	svc := o.Service
	k := endpointKey{ctx: n.id, ip: ip, proto: svc.Proto, port: svc.Port}
	e := c.st.endpoints[k]
	if e != nil && o.Time.Before(e.resultAt) {
		return // late: the endpoint already has a newer result
	}
	hostID := ""
	if holder != nil {
		hostID = holder.id
	}
	detail := o.Meta[observation.MetaDetail]
	prev := observation.ServiceState("")
	insert := e == nil
	if insert {
		c.st.nextEndpointID++
		e = &endpoint{id: c.st.nextEndpointID, key: k, firstSeen: o.Time}
		c.st.endpoints[k] = e
	} else {
		prev = e.result
	}
	changed := insert || prev != svc.State || e.hostID != hostID || e.detail != detail
	e.hostID, e.result, e.resultAt, e.detail = hostID, svc.State, o.Time, detail
	if svc.State.Answered() {
		e.definite, e.definiteAt = svc.State, o.Time
	}
	if svc.State == observation.ServiceOpen {
		e.openAt = o.Time
	}
	c.dbEndpoint(ctx, e, insert)
	if changed {
		c.st.nextEndpointResultID++
		c.exec(ctx, "endpoint result", `INSERT INTO endpoint_results (id, endpoint_id, ts, result, host_id, detail) VALUES (?, ?, ?, ?, ?, ?)`,
			c.st.nextEndpointResultID, e.id, ms(o.Time), string(svc.State), nullString(hostID), nullString(detail))
	}
	if (svc.State == observation.ServiceOpen) == (prev == observation.ServiceOpen) {
		return
	}
	ev.Reason = ""
	if !svc.State.Answered() {
		ev.Reason = "no answer is not a closed port: " + string(svc.State)
	}
	evt := events.Event{TS: o.Time, Type: events.ServiceStateChanged, ContextID: n.id, Interface: n.iface,
		New: svc.Key() + " " + string(svc.State), Cause: string(o.Source), ObservationID: obsID, Evidence: ev}
	if prev != "" {
		evt.Old = svc.Key() + " " + string(prev)
	}
	if holder != nil {
		evt.HostID, evt.MAC = holder.id, holder.mac.String()
	}
	c.emit(ctx, evt)
}

func (c *Correlator) dbEndpoint(ctx context.Context, e *endpoint, insert bool) {
	if insert {
		c.exec(ctx, "insert endpoint", `INSERT INTO endpoints (id, context_id, ip, proto, port, host_id, last_result, last_result_at,
			last_detail, last_open_at, last_definite_result, last_definite_at, first_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.id, e.key.ctx, e.key.ip.String(), e.key.proto, e.key.port, nullString(e.hostID), string(e.result), ms(e.resultAt),
			nullString(e.detail), msOrNil(e.openAt), nullString(string(e.definite)), msOrNil(e.definiteAt), ms(e.firstSeen))
		return
	}
	c.exec(ctx, "update endpoint", `UPDATE endpoints SET host_id = ?, last_result = ?, last_result_at = ?, last_detail = ?,
		last_open_at = ?, last_definite_result = ?, last_definite_at = ? WHERE id = ?`,
		nullString(e.hostID), string(e.result), ms(e.resultAt), nullString(e.detail), msOrNil(e.openAt),
		nullString(string(e.definite)), msOrNil(e.definiteAt), e.id)
}

func (s *state) loadEndpoints(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, context_id, ip, proto, port, coalesce(host_id, ''), last_result, last_result_at,
		coalesce(last_detail, ''), coalesce(last_open_at, 0), coalesce(last_definite_result, ''), coalesce(last_definite_at, 0), first_seen
		FROM endpoints`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		e := &endpoint{}
		var ip, result, definite string
		var at, open, defAt, first int64
		if err := rows.Scan(&e.id, &e.key.ctx, &ip, &e.key.proto, &e.key.port, &e.hostID, &result, &at, &e.detail, &open,
			&definite, &defAt, &first); err != nil {
			return err
		}
		if e.key.ip, err = netip.ParseAddr(ip); err != nil {
			return err
		}
		e.result, e.definite = observation.ServiceState(result), observation.ServiceState(definite)
		e.resultAt, e.openAt, e.definiteAt, e.firstSeen = fromMS(at), optMS(open), optMS(defAt), fromMS(first)
		s.endpoints[e.key] = e
	}
	return rows.Err()
}
