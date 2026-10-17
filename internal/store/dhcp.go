package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// DHCPLease is one row of a client's lease history (docs/DATA_MODEL.md
// §5.6): an address and configuration a server acknowledged. It is the
// server's statement, kept apart from the hosts: it does not say the
// client is present or holds the address.
type DHCPLease struct {
	ID        int64  `json:"id"`
	Interface string `json:"interface"`
	// CHAddr is the client's hardware address from the message; HostID the
	// host with that MAC on the interface, if one was seen.
	CHAddr   string `json:"chaddr"`
	ClientID string `json:"client_id,omitempty"`
	HostID   string `json:"host_id,omitempty"`
	IP       string `json:"ip"`
	ServerID string `json:"server_id,omitempty"`
	// SenderMAC and SenderIP sent the ACK: the server, or a relay.
	SenderMAC    string    `json:"sender_mac,omitempty"`
	SenderIP     string    `json:"sender_ip,omitempty"`
	Relay        string    `json:"relay,omitempty"` // giaddr
	RelayCircuit string    `json:"relay_circuit,omitempty"`
	RelayRemote  string    `json:"relay_remote,omitempty"`
	FirstACK     time.Time `json:"first_ack"`
	LastACK      time.Time `json:"last_ack"`
	ACKs         int       `json:"acks"`
	// LeaseSeconds is -1 for an infinite lease, absent when unknown.
	LeaseSeconds *int64 `json:"lease_seconds,omitempty"`
	// ExpiresAt is the expiry per the last ACK seen: not proof that the
	// address was given up.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	T1        *int64     `json:"t1_seconds,omitempty"`
	T2        *int64     `json:"t2_seconds,omitempty"`
	// TDerived: T1 and T2 were not advertised; they are the RFC 2131
	// defaults.
	TDerived bool `json:"t_derived,omitempty"`
	// Config is the ACK's configuration snapshot. An option it did not
	// carry is absent: unknown, not empty.
	Config map[string]any `json:"config"`
	// Partial: the client's REQUEST was not seen. Incomplete: the ACK was
	// truncated or had malformed options.
	Partial    bool       `json:"partial,omitempty"`
	Incomplete bool       `json:"incomplete,omitempty"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	EndReason  string     `json:"end_reason,omitempty"` // superseded, released, declined
	// Backfilled rows come from LAN Sentinel's observations: the sender,
	// relay, configuration and T1/T2 are unknown.
	Backfilled bool `json:"backfilled,omitempty"`
}

// DHCPLeaseFilter selects lease rows.
type DHCPLeaseFilter struct {
	Interface string
	MAC       string // the client's
	IP        string
	// Open selects only the leases in effect (not ended).
	Open  bool
	Limit int
}

// DHCPOffer is one server's offer in an exchange.
type DHCPOffer struct {
	ServerID  string `json:"server_id,omitempty"`
	YIAddr    string `json:"yiaddr,omitempty"`
	SenderMAC string `json:"sender_mac,omitempty"`
	Count     int    `json:"count"`
}

// DHCPExchange is one DHCP transaction seen (context, xid, chaddr).
type DHCPExchange struct {
	ID        int64     `json:"id"`
	Interface string    `json:"interface"`
	XID       uint32    `json:"xid"`
	CHAddr    string    `json:"chaddr"`
	ClientID  string    `json:"client_id,omitempty"`
	HostID    string    `json:"host_id,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// Messages lists the messages in order: type(server yiaddr)×count.
	Messages       string      `json:"messages"`
	RequestedIP    string      `json:"requested_ip,omitempty"`
	Offers         []DHCPOffer `json:"offers"`
	SelectedServer string      `json:"selected_server,omitempty"`
	// Outcome is the last decisive message (ack, inform_ack, nak, offered,
	// decline, release) or, for an exchange closed unanswered, incomplete.
	// Visibility is complete when both sides were seen (or none was
	// expected), else partial.
	Outcome    string         `json:"outcome"`
	Visibility string         `json:"visibility"`
	Relayed    bool           `json:"relayed,omitempty"`
	Config     map[string]any `json:"config,omitempty"` // an INFORM's ACK
	LeaseID    int64          `json:"lease_id,omitempty"`
}

// DHCPExchangeFilter selects exchanges.
type DHCPExchangeFilter struct {
	Interface string
	MAC       string // the client's
	Since     time.Time
	Limit     int
}

// DefaultDHCPLimit bounds a lease or exchange query without a limit.
const DefaultDHCPLimit = 1000

const leaseColumns = `l.id, c.interface, l.chaddr, coalesce(l.client_id, ''), coalesce(l.host_id, ''), l.ip, l.server_id,
	l.sender_mac, l.sender_ip, l.relay, coalesce(l.relay_circuit, ''), coalesce(l.relay_remote, ''), l.first_ack, l.last_ack, l.acks,
	l.lease_seconds, l.expires_at, l.t1_seconds, l.t2_seconds, l.t_derived, l.config_json, l.partial, l.incomplete, l.ended_at,
	coalesce(l.end_reason, ''), l.backfilled`

// DHCPLeases lists lease rows, newest first.
func (r *Reader) DHCPLeases(ctx context.Context, f DHCPLeaseFilter) ([]DHCPLease, error) {
	var err error
	if f.MAC, err = normalizeFilter(KindMAC, f.MAC); err != nil {
		return nil, err
	}
	if f.IP, err = normalizeFilter(KindIP, f.IP); err != nil {
		return nil, err
	}
	var w where
	if f.Interface != "" {
		w.add(`c.interface = ?`, f.Interface)
	}
	if f.MAC != "" {
		w.add(`l.chaddr = ?`, f.MAC)
	}
	if f.IP != "" {
		w.add(`l.ip = ?`, f.IP)
	}
	if f.Open {
		w.add(`l.ended_at IS NULL`)
	}
	if f.Limit <= 0 {
		f.Limit = DefaultDHCPLimit
	}
	var out []DHCPLease
	err = r.read(ctx, func(tx *sql.Tx) error {
		out, err = leases(ctx, tx, w.sql(), f.Limit, w.args...)
		return err
	})
	return out, err
}

// leases loads lease rows matching the WHERE clause cond, newest first.
func leases(ctx context.Context, tx *sql.Tx, cond string, limit int, args ...any) ([]DHCPLease, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+leaseColumns+` FROM dhcp_leases l JOIN network_contexts c ON c.id = l.context_id`+
		cond+` ORDER BY l.first_ack DESC, l.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DHCPLease{}
	for rows.Next() {
		var l DHCPLease
		var first, last int64
		var lease, expires, t1, t2, ended sql.NullInt64
		var derived, partial, incomplete, backfilled int
		var cfg string
		if err := rows.Scan(&l.ID, &l.Interface, &l.CHAddr, &l.ClientID, &l.HostID, &l.IP, &l.ServerID, &l.SenderMAC, &l.SenderIP,
			&l.Relay, &l.RelayCircuit, &l.RelayRemote, &first, &last, &l.ACKs, &lease, &expires, &t1, &t2, &derived, &cfg,
			&partial, &incomplete, &ended, &l.EndReason, &backfilled); err != nil {
			return nil, err
		}
		l.FirstACK, l.LastACK, l.ExpiresAt, l.EndedAt = timeOf(first), timeOf(last), timePtr(expires), timePtr(ended)
		l.LeaseSeconds, l.T1, l.T2 = intPtr(lease), intPtr(t1), intPtr(t2)
		l.TDerived, l.Partial, l.Incomplete, l.Backfilled = derived == 1, partial == 1, incomplete == 1, backfilled == 1
		if err := json.Unmarshal([]byte(cfg), &l.Config); err != nil || l.Config == nil {
			l.Config = map[string]any{}
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func intPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

// DHCPExchanges lists exchanges, newest first.
func (r *Reader) DHCPExchanges(ctx context.Context, f DHCPExchangeFilter) ([]DHCPExchange, error) {
	var err error
	if f.MAC, err = normalizeFilter(KindMAC, f.MAC); err != nil {
		return nil, err
	}
	var w where
	if f.Interface != "" {
		w.add(`c.interface = ?`, f.Interface)
	}
	if f.MAC != "" {
		w.add(`x.chaddr = ?`, f.MAC)
	}
	if !f.Since.IsZero() {
		w.add(`x.last_seen >= ?`, msOf(f.Since))
	}
	if f.Limit <= 0 {
		f.Limit = DefaultDHCPLimit
	}
	out := []DHCPExchange{}
	err = r.read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT x.id, c.interface, x.xid, x.chaddr, coalesce(x.client_id, ''), coalesce(x.host_id, ''),
			x.first_seen, x.last_seen, x.messages, coalesce(x.requested_ip, ''), x.offers_json, coalesce(x.selected_server, ''), x.outcome,
			x.visibility, x.relayed, x.config_json, coalesce(x.lease_id, 0)
			FROM dhcp_exchanges x JOIN network_contexts c ON c.id = x.context_id`+w.sql()+` ORDER BY x.first_seen DESC, x.id DESC LIMIT ?`,
			append(w.args, f.Limit)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x DHCPExchange
			var first, last int64
			var relayed int
			var offers string
			var cfg sql.NullString
			if err := rows.Scan(&x.ID, &x.Interface, &x.XID, &x.CHAddr, &x.ClientID, &x.HostID, &first, &last, &x.Messages, &x.RequestedIP,
				&offers, &x.SelectedServer, &x.Outcome, &x.Visibility, &relayed, &cfg, &x.LeaseID); err != nil {
				return err
			}
			x.FirstSeen, x.LastSeen, x.Relayed = timeOf(first), timeOf(last), relayed == 1
			if err := json.Unmarshal([]byte(offers), &x.Offers); err != nil || x.Offers == nil {
				x.Offers = []DHCPOffer{}
			}
			if cfg.Valid {
				_ = json.Unmarshal([]byte(cfg.String), &x.Config)
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, err
}
