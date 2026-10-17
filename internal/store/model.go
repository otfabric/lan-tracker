package store

import (
	"encoding/json"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// The read model: what the API returns and the CLI prints. JSON field names
// are the API contract (docs/API.md).

// HostSummary is one row of the inventory.
type HostSummary struct {
	HostID    string `json:"host_id"`
	Interface string `json:"interface"`
	MAC       string `json:"mac"`
	// Vendor is the IEEE registrant of the MAC prefix (OUI): a hint about
	// the network interface, never the equipment's manufacturer.
	Vendor              string `json:"oui_registrant,omitempty"`
	LocallyAdministered bool   `json:"locally_administered"`
	Presence            string `json:"presence"`
	PreferredName       string `json:"preferred_name,omitempty"`
	// ProxyARP: the MAC answered ARP for addresses other live hosts held
	// (docs/DATA_MODEL.md §5.2), so its ARP replies prove nothing.
	ProxyARP    bool      `json:"proxy_arp,omitempty"`
	Description string    `json:"description,omitempty"` // set by an operator (docs/DATA_MODEL.md §5.8)
	IPs         []string  `json:"ips"`                   // open address bindings
	FirstSeen   time.Time `json:"first_seen"`
	// LastSeen is the latest presence evidence of any kind; the kinds
	// apart (docs/DATA_MODEL.md §2): a frame directly observed from the
	// MAC, an address confirmed at it, an endpoint it held alone that
	// answered. Unknown (absent) for hosts recorded by LAN Sentinel.
	LastSeen             time.Time  `json:"last_seen"`
	LastFrameSeen        *time.Time `json:"last_frame_seen,omitempty"`
	LastAddressConfirmed *time.Time `json:"last_address_confirmed,omitempty"`
	LastEndpointAnswer   *time.Time `json:"last_endpoint_answer,omitempty"`
}

// Binding is an address binding with its interval (docs/DATA_MODEL.md §3).
type Binding struct {
	IP        string       `json:"ip"`
	FirstSeen time.Time    `json:"first_seen"`
	LastSeen  time.Time    `json:"last_seen"`
	EndedAt   *time.Time   `json:"ended_at"`
	Conflict  bool         `json:"conflict"`
	Sources   []SourceSeen `json:"sources,omitempty"`
}

// SourceSeen says when a source confirmed an attribute.
type SourceSeen struct {
	Source    string    `json:"source"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// Name is a name record (docs/DATA_MODEL.md §5.4): what was advertised or
// resolved, by whom, and how it is linked to a MAC.
type Name struct {
	Name       string `json:"name"`
	Type       string `json:"type"`        // name type: mdns, dns, dns_ptr, dhcp, lldp
	RecordType string `json:"record_type"` // A, AAAA, PTR, SRV, TXT, CNAME, NAME
	// Address is what an A, AAAA or PTR record describes; Value a CNAME or
	// SRV target, TXT text or a service instance.
	Address string `json:"address,omitempty"`
	Value   string `json:"value,omitempty"`
	Source  string `json:"source"`
	// The advertiser: the frame's sender (none for a resolver's answer).
	AdvertiserMAC string `json:"advertiser_mac,omitempty"`
	AdvertiserIP  string `json:"advertiser_ip,omitempty"`
	// Link says how the record is tied to HostID: sender (a name the device
	// gave itself), address (one MAC alone held the address), none, legacy
	// (LAN Sentinel's name, without its record type, advertiser or TTL).
	Link      string     `json:"link"`
	HostID    string     `json:"host_id,omitempty"`
	TTL       *int64     `json:"ttl_seconds,omitempty"`
	FirstSeen time.Time  `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at"`
	EndReason string     `json:"end_reason,omitempty"`
	Legacy    bool       `json:"legacy,omitempty"`
	// Freshness when read: fresh, expired (its TTL ran out since it was
	// last seen), stale (no TTL and not seen for identity.name_expiry) or
	// ended. Expired and stale names stay in effect: time alone never
	// ends a name.
	Freshness string `json:"freshness"`
}

// Endpoint is one TCP endpoint's results (docs/DATA_MODEL.md §5.5): an
// address and port on an interface, with the MAC that alone held the
// address at the last result (none when no MAC or several did). The last
// result and the last definite answer are kept apart: a timeout never
// erases that the endpoint last answered OPEN. OPEN is a completed connect
// at that time, not proof of any protocol or device.
type Endpoint struct {
	Interface          string     `json:"interface"`
	IP                 string     `json:"ip"`
	Proto              string     `json:"proto"`
	Port               int        `json:"port"`
	HostID             string     `json:"host_id,omitempty"`
	MAC                string     `json:"mac,omitempty"`
	LastResult         string     `json:"last_result"`
	LastResultAt       time.Time  `json:"last_result_at"`
	LastDetail         string     `json:"last_detail,omitempty"`
	LastOpenAt         *time.Time `json:"last_open_at,omitempty"`
	LastDefiniteResult string     `json:"last_definite_result,omitempty"`
	LastDefiniteAt     *time.Time `json:"last_definite_at,omitempty"`
	FirstSeen          time.Time  `json:"first_seen"`
}

// Service is a LAN Sentinel probe result for one protocol and port of a
// host, without its address (legacy, read only).
type Service struct {
	Proto        string    `json:"proto"`
	Port         int       `json:"port"`
	State        string    `json:"state"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	LastResultAt time.Time `json:"last_result_at"`
}

// Identification is a claim LAN Sentinel recorded (legacy, ADR 0012): kept
// readable as history, never updated by LAN Tracker.
type Identification struct {
	Field      string          `json:"field"`
	Value      string          `json:"value"`
	Confidence float64         `json:"confidence"` // the strongest evidence seen for the value
	Source     string          `json:"source"`
	Evidence   json.RawMessage `json:"evidence"`
	FirstSeen  time.Time       `json:"first_seen"`
	LastSeen   time.Time       `json:"last_seen"`
	Current    bool            `json:"current"`
}

// Host is a full host record.
type Host struct {
	HostSummary
	Addresses []Binding `json:"addresses"`
	Names     []Name    `json:"names"`
	// Endpoints are the TCP endpoints attributed to the host at their last
	// result.
	Endpoints []Endpoint `json:"endpoints"`
	// DHCPLeases are the leases servers acknowledged for the host's MAC on
	// its interface, newest first: what the servers said, not proof the
	// host holds the address.
	DHCPLeases []DHCPLease `json:"dhcp_leases"`
	// LegacyServices are LAN Sentinel's results, without an address.
	LegacyServices []Service `json:"legacy_services,omitempty"`
	// LegacyIdentifications are LAN Sentinel's identification rows.
	LegacyIdentifications []Identification `json:"legacy_identifications,omitempty"`
}

// Event is a stored event (docs/DATA_MODEL.md §7).
type Event struct {
	ID            int64           `json:"id,omitempty"`
	TS            time.Time       `json:"ts"`
	Type          string          `json:"type"`
	Severity      string          `json:"severity"`
	Interface     string          `json:"interface,omitempty"`
	HostID        string          `json:"host_id,omitempty"`
	MAC           string          `json:"mac,omitempty"`
	RelatedHostID string          `json:"related_host_id,omitempty"`
	RelatedMAC    string          `json:"related_mac,omitempty"`
	Old           string          `json:"old,omitempty"`
	New           string          `json:"new,omitempty"`
	Cause         string          `json:"cause"`
	ObservationID int64           `json:"observation_id,omitempty"`
	Evidence      json.RawMessage `json:"evidence"`
	// ClockSync is the kernel clock's state when the event was written:
	// synced, unsynced (before NTP synchronisation) or unknown.
	ClockSync string `json:"clock_sync"`
}

// EvidenceIP returns the IP of the event's evidence snapshot.
func (e Event) EvidenceIP() string {
	var ev struct {
		IP string `json:"ip"`
	}
	_ = json.Unmarshal(e.Evidence, &ev)
	return ev.IP
}

// Observation is a stored raw observation.
type Observation struct {
	ID        int64           `json:"id"`
	TS        time.Time       `json:"ts"`
	Interface string          `json:"interface"`
	Source    string          `json:"source"`
	MAC       string          `json:"mac,omitempty"`
	IP        string          `json:"ip,omitempty"`
	Hostname  string          `json:"hostname,omitempty"`
	NameType  string          `json:"name_type,omitempty"`
	Service   json.RawMessage `json:"service,omitempty"`
	Meta      json.RawMessage `json:"meta,omitempty"`
	HostID    string          `json:"host_id,omitempty"`
}

// Rollup is an hourly roll-up of observations.
type Rollup struct {
	Hour      time.Time `json:"hour"`
	Interface string    `json:"interface"`
	HostID    string    `json:"host_id,omitempty"`
	Source    string    `json:"source"`
	MAC       string    `json:"mac,omitempty"`
	IP        string    `json:"ip,omitempty"`
	Count     int64     `json:"count"`
	FirstTS   time.Time `json:"first_ts"`
	LastTS    time.Time `json:"last_ts"`
}

// DHCPServer is a DHCP server identity seen on an interface through one
// relay (or none) from one sender (docs/DATA_MODEL.md §5.6).
type DHCPServer struct {
	Interface string `json:"interface"`
	// ServerID is the server identifier (option 54); empty when the replies
	// had none or an invalid one, so the server's identity is unknown.
	ServerID string `json:"server_id,omitempty"`
	Relay    string `json:"relay,omitempty"` // giaddr of relayed replies
	// MAC and IP are the replies' sender: the server, or the relay.
	MAC    string `json:"mac"`
	IP     string `json:"ip,omitempty"`
	HostID string `json:"host_id,omitempty"`
	// Status is the allowlist verdict at the last reply: allowed,
	// unexpected, or unchecked (no allowlist on the interface).
	Status string `json:"status"`
	// Config is what the server last advertised: router, dns, subnet_mask.
	Config    map[string]string `json:"config"`
	FirstSeen time.Time         `json:"first_seen"`
	LastSeen  time.Time         `json:"last_seen"`
}

// DHCPServerFilter selects DHCP servers.
type DHCPServerFilter struct {
	Interface string
	Status    string
}

// InterfaceInfo is one monitored interface (network context).
type InterfaceInfo struct {
	Name      string    `json:"name"`
	State     string    `json:"state"` // up, down or unknown
	MAC       string    `json:"mac,omitempty"`
	Prefixes  []string  `json:"prefixes"`
	Passive   bool      `json:"passive"`
	Active    bool      `json:"active"`
	Replay    bool      `json:"replay,omitempty"`
	Hosts     int       `json:"hosts"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// DBInfo describes the database (`db info`).
type DBInfo struct {
	Path          string           `json:"path"`
	SchemaVersion int              `json:"schema_version"`
	JournalMode   string           `json:"journal_mode"`
	Size          int64            `json:"size"`
	UsedSize      int64            `json:"used_size"`
	WALSize       int64            `json:"wal_size"`
	Rows          map[string]int64 `json:"rows,omitempty"`
}

// CheckResult is the outcome of `db check`.
type CheckResult struct {
	OK       bool     `json:"ok"`
	Problems []string `json:"problems,omitempty"`
}

// QueryKind classifies a host query (docs/CLI.md §4).
type QueryKind string

// Query kinds.
const (
	KindHost QueryKind = "host"
	KindMAC  QueryKind = "mac"
	KindIP   QueryKind = "ip"
	KindName QueryKind = "name"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Classify detects the kind of a query: host UUID, MAC (aa:bb:.., aa-bb-..
// or aabb.ccdd.eeff), IPv4, IPv6, else hostname. It returns the query in
// the form the database stores.
func Classify(q string) (QueryKind, string) {
	q = strings.TrimSpace(q)
	if uuidRE.MatchString(q) {
		return KindHost, strings.ToLower(q)
	}
	if m, err := net.ParseMAC(q); err == nil && len(m) == 6 {
		return KindMAC, m.String()
	}
	if ip, err := netip.ParseAddr(q); err == nil {
		return KindIP, ip.Unmap().String()
	}
	return KindName, q
}

// Normalize returns q in stored form for an explicit kind, or classifies
// it when kind is empty. It reports false when q is not of that kind.
func Normalize(kind QueryKind, q string) (QueryKind, string, bool) {
	detected, norm := Classify(q)
	if kind == "" || kind == detected {
		return detected, norm, true
	}
	if kind == KindName {
		return KindName, strings.TrimSpace(q), true
	}
	return kind, q, false
}

// HostFilter selects hosts for `hosts list`.
type HostFilter struct {
	Interface string
	// Query restricts to hosts that ever matched it (any kind).
	Query       string
	QueryKind   QueryKind
	Live        bool   // ACTIVE or RECENT
	NotLive     bool   // STALE or MISSING
	Vendor      string // the OUI registrant contains this text
	Description string // the description contains this text
	Port        int    // an endpoint of the host on this TCP port last answered OPEN
	SeenSince   time.Time
}

// FindQuery is a `hosts find` query.
type FindQuery struct {
	Query     string
	Kind      QueryKind
	Interface string
	At        *time.Time
}

// FindResult answers `hosts find` (docs/CLI.md §4).
type FindResult struct {
	Kind  QueryKind   `json:"kind"`
	Query string      `json:"query"`
	At    *time.Time  `json:"at,omitempty"`
	Hosts []FoundHost `json:"hosts"`
	// For an IP with no holder at At: the binding before and after, per
	// interface.
	Previous []Holding `json:"previous,omitempty"`
	Next     []Holding `json:"next,omitempty"`
	// Advertised are the name records of a name query linked to no MAC:
	// what was advertised or resolved, unmapped.
	Advertised []Name `json:"advertised,omitempty"`
}

// FoundHost is one holder or match.
type FoundHost struct {
	Host
	// IP queries: the binding of the queried address.
	Binding     *Binding `json:"binding,omitempty"`
	Unconfirmed bool     `json:"unconfirmed,omitempty"`
	Conflict    bool     `json:"conflict,omitempty"`
	// With At: the next holder of the address, and the first address the
	// host gained after this binding ended. A MAC's addresses are
	// independent (docs/DATA_MODEL.md §5.3): NextAddress is not proof that
	// the device moved.
	ReplacedBy    *Holding `json:"replaced_by,omitempty"`
	NextAddress   *Holding `json:"next_address,omitempty"`
	ConflictEvent *Event   `json:"conflict_event,omitempty"`
}

// Holding is a binding with its holder.
type Holding struct {
	Interface string  `json:"interface"`
	HostID    string  `json:"host_id"`
	MAC       string  `json:"mac"`
	Binding   Binding `json:"binding"`
}

// HistoryQuery selects a timeline (`hosts history`).
type HistoryQuery struct {
	Query     string
	Kind      QueryKind
	Interface string
	Since     time.Time
	Until     time.Time
}

// EventFilter selects events (`events list`).
type EventFilter struct {
	Since, Until time.Time
	Types        []string // spec type names
	Interface    string
	MAC          string
	IP           string
	HostID       string
	Limit        int
}

// ObservationFilter selects observations (`observations list`).
type ObservationFilter struct {
	HostID       string
	Interface    string
	MAC          string
	IP           string
	Source       string
	Unbound      bool
	Since, Until time.Time
	Limit        int
}

// ServiceFilter selects endpoints (`services list`). State matches the
// last result; At reads the results in effect then.
type ServiceFilter struct {
	Port      int
	State     string
	Interface string
	IP        string
	At        *time.Time
}

// Evidence answers `hosts evidence`: the host, per source and address the
// observation counts within roll-up retention, and the host's events with
// their evidence snapshots.
type Evidence struct {
	Host   Host          `json:"host"`
	Counts []SourceCount `json:"counts"`
	Events []Event       `json:"events"`
}

// SourceCount counts the observations of one source for one address.
type SourceCount struct {
	Source    string    `json:"source"`
	IP        string    `json:"ip,omitempty"`
	Count     int64     `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}
