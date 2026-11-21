package store

import (
	"context"
	"database/sql"
)

// EvictedHost is the row of a host the correlator does not hold (the host
// cap, ADR 0013, docs/DATA_MODEL.md §5.9): what it needs to hold the host
// again or to describe it. Times are Unix milliseconds as stored, 0 when
// unknown.
type EvictedHost struct {
	HostID, Interface, MAC                                  string
	ContextID                                               int64
	OUIRegistrant, PreferredName, Description               string
	ProxyARP                                                bool
	FirstSeen, LastSeen                                     int64
	LastFrameSeen, LastAddressConfirmed, LastEndpointAnswer int64
}

const evictedHostQuery = `SELECT h.host_id, c.interface, h.mac, h.context_id, coalesce(h.oui_registrant, ''),
	coalesce(h.preferred_name, ''), coalesce(h.description, ''), h.proxy_arp, h.first_seen, h.last_seen,
	coalesce(h.last_frame_seen, 0), coalesce(h.last_address_confirmed, 0), coalesce(h.last_endpoint_answer, 0)
	FROM hosts h JOIN network_contexts c ON c.id = h.context_id `

func scanEvictedHost(row *sql.Row) (EvictedHost, error) {
	var h EvictedHost
	err := row.Scan(&h.HostID, &h.Interface, &h.MAC, &h.ContextID, &h.OUIRegistrant, &h.PreferredName, &h.Description, &h.ProxyARP,
		&h.FirstSeen, &h.LastSeen, &h.LastFrameSeen, &h.LastAddressConfirmed, &h.LastEndpointAnswer)
	return h, err
}

// HostRow reads the committed row of mac on a network context; the error
// wraps sql.ErrNoRows when the MAC was never a host there. The correlator
// reads an evicted host back through it without making the writer commit.
func (r *Reader) HostRow(ctx context.Context, contextID int64, mac string) (EvictedHost, error) {
	var h EvictedHost
	err := r.read(ctx, func(tx *sql.Tx) error {
		var err error
		h, err = scanEvictedHost(tx.QueryRowContext(ctx, evictedHostQuery+`WHERE h.context_id = ? AND h.mac = ?`, contextID, mac))
		return err
	})
	return h, err
}

// EvictedHostRow reads the committed row of an evicted host by its id; the
// error wraps sql.ErrNoRows when there is no such evicted host.
func (r *Reader) EvictedHostRow(ctx context.Context, hostID string) (EvictedHost, error) {
	var h EvictedHost
	err := r.read(ctx, func(tx *sql.Tx) error {
		var err error
		h, err = scanEvictedHost(tx.QueryRowContext(ctx, evictedHostQuery+`WHERE h.host_id = ? AND h.evicted_at IS NOT NULL`, hostID))
		return err
	})
	return h, err
}
