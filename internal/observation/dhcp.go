package observation

import (
	"net/netip"
	"slices"
)

// DHCP message types (option 53).
const (
	DHCPDiscover = "discover"
	DHCPOffer    = "offer"
	DHCPRequest  = "request"
	DHCPDecline  = "decline"
	DHCPAck      = "ack"
	DHCPNak      = "nak"
	DHCPRelease  = "release"
	DHCPInform   = "inform"
)

// DHCPMessage is a decoded DHCPv4 message (docs/DATA_MODEL.md §5.6): the
// BOOTP header and the options LAN Tracker interprets, from the options
// field and, when option 52 overloads them, the file and sname fields,
// with options split over several instances joined (RFC 3396). Present
// lists the option codes the message carried: an option it did not carry
// is unknown, never empty, so a reply that leaves out the router says
// nothing about the router. Malformed lists options that did not parse;
// Truncated marks a message that ended before its end option, whose
// configuration is incomplete. None of it identifies a device: the client
// identifier, hostname and FQDN are what the client said, never merge keys.
type DHCPMessage struct {
	Op        int    `json:"op"` // 1 BOOTREQUEST, 2 BOOTREPLY
	Type      string `json:"type"`
	XID       uint32 `json:"xid"`
	Secs      uint16 `json:"secs,omitempty"`
	Broadcast bool   `json:"broadcast,omitempty"`
	// Hops is the BOOTP hop count: set by relays.
	Hops   int        `json:"hops,omitempty"`
	CIAddr netip.Addr `json:"ciaddr,omitzero"`
	YIAddr netip.Addr `json:"yiaddr,omitzero"`
	SIAddr netip.Addr `json:"siaddr,omitzero"`
	GIAddr netip.Addr `json:"giaddr,omitzero"`
	// CHAddr is the client's hardware address when it is Ethernet (htype 1,
	// hlen 6); else empty, and the message stays DHCP evidence without a
	// MAC.
	CHAddr string `json:"chaddr,omitempty"`

	ClientID     string       `json:"client_id,omitempty"` // option 61, hex
	Hostname     string       `json:"hostname,omitempty"`  // 12
	FQDN         *DHCPFQDN    `json:"fqdn,omitempty"`      // 81
	RequestedIP  netip.Addr   `json:"requested_ip,omitzero"`
	ServerID     netip.Addr   `json:"server_id,omitzero"` // 54; invalid when absent or unusable
	SubnetMask   netip.Addr   `json:"subnet_mask,omitzero"`
	Lease        *uint32      `json:"lease_seconds,omitempty"`       // 51; 0xffffffff is infinite
	T1           *uint32      `json:"t1_seconds,omitempty"`          // 58
	T2           *uint32      `json:"t2_seconds,omitempty"`          // 59
	Routers      []netip.Addr `json:"routers,omitempty"`             // 3
	DNS          []netip.Addr `json:"dns,omitempty"`                 // 6
	Domain       string       `json:"domain,omitempty"`              // 15
	Search       []string     `json:"search,omitempty"`              // 119
	Routes       []DHCPRoute  `json:"classless_routes,omitempty"`    // 121
	MSRoutes     []DHCPRoute  `json:"ms_classless_routes,omitempty"` // 249, a pre-standard copy of 121
	RelayAgent   *DHCPRelay   `json:"relay_agent,omitempty"`         // 82
	ParamRequest []int        `json:"parameter_request_list,omitempty"`
	VendorClass  string       `json:"vendor_class,omitempty"`
	Present      []int        `json:"present,omitempty"`
	Malformed    []int        `json:"malformed,omitempty"`
	Truncated    bool         `json:"truncated,omitempty"`
}

// DHCPFQDN is the client FQDN option (RFC 4702).
type DHCPFQDN struct {
	Flags uint8  `json:"flags"` // S, O, E, N
	Name  string `json:"name,omitempty"`
}

// DHCPRoute is one classless static route (RFC 3442).
type DHCPRoute struct {
	Destination netip.Prefix `json:"destination"`
	Router      netip.Addr   `json:"router"`
}

// DHCPRelay holds the relay agent information sub-options (RFC 3046), as
// hex.
type DHCPRelay struct {
	CircuitID string `json:"circuit_id,omitempty"`
	RemoteID  string `json:"remote_id,omitempty"`
}

// Has reports whether the message carried option code.
func (m *DHCPMessage) Has(code int) bool { return slices.Contains(m.Present, code) }

// Relayed reports a message a relay forwarded (giaddr set): it describes a
// client that may sit on another link.
func (m *DHCPMessage) Relayed() bool { return m.GIAddr.IsValid() && !m.GIAddr.IsUnspecified() }

// Allocates reports an ACK that assigns or renews an address: yiaddr set.
// The ACK to an INFORM (yiaddr 0) only carries configuration.
func (m *DHCPMessage) Allocates() bool {
	return m.Type == DHCPAck && m.YIAddr.IsValid() && !m.YIAddr.IsUnspecified()
}

// Gateway is the default gateway the message advertises and where it comes
// from: with option 121 present its default route (a client must then
// ignore option 3, RFC 3442), else the first router of option 3. "" when
// the message advertises none or does not say.
func (m *DHCPMessage) Gateway() (netip.Addr, string) {
	if m.Has(121) {
		for _, r := range m.Routes {
			if r.Destination.Bits() == 0 {
				return r.Router, "option_121"
			}
		}
		return netip.Addr{}, "option_121"
	}
	if len(m.Routers) > 0 {
		return m.Routers[0], "option_3"
	}
	return netip.Addr{}, ""
}
