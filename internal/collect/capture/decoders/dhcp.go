package decoders

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"

	"github.com/otfabric/lan-tracker/internal/observation"
)

// DHCP option codes.
const (
	dhcpOptPad          = 0
	dhcpOptSubnetMask   = 1
	dhcpOptRouter       = 3
	dhcpOptDNS          = 6
	dhcpOptHostname     = 12
	dhcpOptDomain       = 15
	dhcpOptRequestedIP  = 50
	dhcpOptLeaseTime    = 51
	dhcpOptOverload     = 52
	dhcpOptMessageType  = 53
	dhcpOptServerID     = 54
	dhcpOptParamRequest = 55
	dhcpOptT1           = 58
	dhcpOptT2           = 59
	dhcpOptVendorClass  = 60
	dhcpOptClientID     = 61
	dhcpOptFQDN         = 81
	dhcpOptRelayAgent   = 82
	dhcpOptSearch       = 119
	dhcpOptRoutes       = 121
	dhcpOptMSRoutes     = 249
	dhcpOptEnd          = 255
	dhcpMagic           = 0x63825363
	dhcpFixedLen        = 240 // BOOTP header and magic cookie

	// Bounds on what one message contributes.
	maxOptionBytes  = 1024 // per option code, after joining (RFC 3396)
	maxClientID     = 64   // bytes of option 61 kept
	maxParamRequest = 128  // codes of option 55 kept
	maxAddrList     = 8    // addresses of options 3 and 6 kept
	maxRoutes       = 32   // routes of options 121 and 249
	maxSearch       = 8    // domains of option 119
	maxPointers     = 32   // compression pointers followed per domain (option 119)
)

// dhcpTypes maps option 53 to the message type and the BOOTP op it needs.
var dhcpTypes = map[byte]struct {
	name string
	op   byte
}{
	1: {observation.DHCPDiscover, 1}, 2: {observation.DHCPOffer, 2}, 3: {observation.DHCPRequest, 1}, 4: {observation.DHCPDecline, 1},
	5: {observation.DHCPAck, 2}, 6: {observation.DHCPNak, 2}, 7: {observation.DHCPRelease, 1}, 8: {observation.DHCPInform, 1},
}

// dhcp decodes a DHCPv4 message sent from src (FR-PA-3, FR-PA-7) into one
// observation with the decoded message (observation.DHCPMessage). A
// message from a client (BOOTREQUEST) is passive_dhcp. Its sender is the
// client only when the frame came straight from it (its Ethernet source is
// chaddr and no relay forwarded it): then it carries the client's own
// claims, ciaddr in a REQUEST or INFORM and its hostname. A relayed or
// otherwise indirect request describes a client that may sit on another
// link: the observation's MAC and address are the sender's, and the client
// is only in the message. A server's reply (BOOTREPLY) is
// passive_dhcp_server, from the server or the relay that forwarded it. The
// direction is decided by the BOOTP op and the message type agreeing, not
// by ports, so replies relayed between port 67 and 67 decode too.
func (f *frame) dhcp(src netip.Addr, b []byte) {
	m, ok := parseDHCP(b)
	if !ok {
		return
	}
	if m.Op == 2 {
		if f.src != nil {
			f.add(observation.Observation{Source: observation.PassiveDHCPServer, MAC: f.src, IP: src, DHCP: m})
		}
		return
	}
	o := observation.Observation{Source: observation.PassiveDHCP, MAC: f.src, IP: src, DHCP: m}
	if !m.Relayed() && f.src != nil && m.CHAddr == f.src.String() {
		o.IP = netip.Addr{}
		if m.Type == observation.DHCPRequest || m.Type == observation.DHCPInform {
			o.IP = m.CIAddr // the client's claim, if it has an address; 0.0.0.0 is dropped by add
		}
		if m.Hostname != "" {
			o.Hostname, o.NameType = m.Hostname, observation.NameDHCP
		}
	}
	f.add(o)
}

// parseDHCP decodes a DHCPv4 message; false for BOOTP, an unknown message
// type, or an op that disagrees with it.
func parseDHCP(b []byte) (*observation.DHCPMessage, bool) {
	if len(b) < dhcpFixedLen || binary.BigEndian.Uint32(b[236:240]) != dhcpMagic {
		return nil, false
	}
	opts, present, truncated := dhcpGather(b)
	mt := opts[dhcpOptMessageType]
	if len(mt) != 1 {
		return nil, false
	}
	t, ok := dhcpTypes[mt[0]]
	if !ok || b[0] != t.op {
		return nil, false
	}
	m := &observation.DHCPMessage{
		Op: int(b[0]), Type: t.name, Hops: int(b[3]), XID: binary.BigEndian.Uint32(b[4:8]), Secs: be16(b[8:]),
		Broadcast: be16(b[10:])&0x8000 != 0, CIAddr: addr4(b[12:]), YIAddr: addr4(b[16:]), SIAddr: addr4(b[20:]), GIAddr: addr4(b[24:]),
		Present: present, Truncated: truncated,
	}
	if b[1] == 1 && b[2] == 6 {
		m.CHAddr = net.HardwareAddr(b[28:34]).String()
	}
	bad := func(code int) { m.Malformed = append(m.Malformed, code) }
	addr := func(code int) netip.Addr {
		v, ok := opts[byte(code)]
		if !ok {
			return netip.Addr{}
		}
		if len(v) != 4 {
			bad(code)
			return netip.Addr{}
		}
		return addr4(v)
	}
	seconds := func(code int) *uint32 {
		v, ok := opts[byte(code)]
		if !ok {
			return nil
		}
		if len(v) != 4 {
			bad(code)
			return nil
		}
		s := binary.BigEndian.Uint32(v)
		return &s
	}
	list := func(code int) []netip.Addr {
		v, ok := opts[byte(code)]
		if !ok {
			return nil
		}
		if len(v) == 0 || len(v)%4 != 0 {
			bad(code)
			return nil
		}
		out := make([]netip.Addr, 0, min(len(v)/4, maxAddrList))
		for i := 0; i < len(v) && len(out) < maxAddrList; i += 4 {
			out = append(out, addr4(v[i:]))
		}
		return out
	}
	if id := addr(dhcpOptServerID); usableIP(id) {
		m.ServerID = id
	} else if id.IsValid() {
		bad(dhcpOptServerID) // 0.0.0.0, broadcast or multicast: no identity
	}
	m.SubnetMask, m.RequestedIP = addr(dhcpOptSubnetMask), addr(dhcpOptRequestedIP)
	m.Lease, m.T1, m.T2 = seconds(dhcpOptLeaseTime), seconds(dhcpOptT1), seconds(dhcpOptT2)
	m.Routers, m.DNS = list(dhcpOptRouter), list(dhcpOptDNS)
	m.Hostname = hostname(string(opts[dhcpOptHostname]))
	if v, ok := opts[dhcpOptDomain]; ok {
		if m.Domain = hostname(string(v)); m.Domain == "" {
			bad(dhcpOptDomain)
		}
	}
	if v, ok := opts[dhcpOptSearch]; ok {
		var err error
		if m.Search, err = domainList(v); err != nil {
			bad(dhcpOptSearch)
		}
	}
	if v, ok := opts[dhcpOptFQDN]; ok {
		if fq, err := clientFQDN(v); err == nil {
			m.FQDN = fq
		} else {
			bad(dhcpOptFQDN)
		}
	}
	for _, rc := range []struct {
		code int
		dst  *[]observation.DHCPRoute
	}{{dhcpOptRoutes, &m.Routes}, {dhcpOptMSRoutes, &m.MSRoutes}} {
		if v, ok := opts[byte(rc.code)]; ok {
			var err error
			if *rc.dst, err = classlessRoutes(v); err != nil {
				bad(rc.code)
			}
		}
	}
	if v, ok := opts[dhcpOptRelayAgent]; ok {
		if ra, err := relayAgent(v); err == nil {
			m.RelayAgent = ra
		} else {
			bad(dhcpOptRelayAgent)
		}
	}
	if id := opts[dhcpOptClientID]; len(id) > 0 {
		m.ClientID = hexBytes(id[:min(len(id), maxClientID)])
	}
	for _, c := range opts[dhcpOptParamRequest][:min(len(opts[dhcpOptParamRequest]), maxParamRequest)] {
		m.ParamRequest = append(m.ParamRequest, int(c))
	}
	m.VendorClass = text(opts[dhcpOptVendorClass], maxText)
	return m, true
}

// dhcpGather collects the options of the options field and, as option 52
// says, of the file and sname fields, in that order (RFC 2131 §4.1). A
// code found several times is joined (RFC 3396), up to maxOptionBytes. The
// options field must end with the end option: truncated reports one that
// did not, or an option cut short anywhere.
func dhcpGather(b []byte) (map[byte][]byte, []int, bool) {
	opts := map[byte][]byte{}
	var present []int
	truncated := false
	parse := func(field []byte, needEnd bool) {
		for len(field) > 0 {
			code := field[0]
			switch code {
			case dhcpOptEnd:
				return
			case dhcpOptPad:
				field = field[1:]
				continue
			}
			if len(field) < 2 || len(field) < 2+int(field[1]) {
				truncated = true
				return
			}
			n := int(field[1])
			if !slices.Contains(present, int(code)) {
				present = append(present, int(code))
			}
			if room := maxOptionBytes - len(opts[code]); room > 0 {
				opts[code] = append(opts[code], field[2:2+min(n, room)]...)
			}
			field = field[2+n:]
		}
		if needEnd {
			truncated = true
		}
	}
	parse(b[dhcpFixedLen:], true)
	if ov := opts[dhcpOptOverload]; len(ov) == 1 {
		if ov[0]&1 != 0 {
			parse(b[108:236], false)
		}
		if ov[0]&2 != 0 {
			parse(b[44:108], false)
		}
	}
	slices.Sort(present)
	return opts, present, truncated
}

// domainList decodes option 119 (RFC 3397): DNS names in wire format
// with compression pointers into the option's data, each pointer pointing
// backwards, at most maxPointers per name and maxSearch names.
func domainList(v []byte) ([]string, error) {
	var out []string
	for off := 0; off < len(v) && len(out) < maxSearch; {
		name, next, err := wireName(v, off)
		if err != nil {
			return out, err
		}
		if name != "" {
			out = append(out, name)
		}
		off = next
	}
	return out, nil
}

// wireName reads the DNS name at off in v; it returns the name and where
// the data after it starts.
func wireName(v []byte, off int) (string, int, error) {
	var labels []string
	next, jumps, pos := -1, 0, off
	for {
		if pos >= len(v) {
			return "", 0, fmt.Errorf("name runs past the option")
		}
		l := int(v[pos])
		switch {
		case l == 0:
			if next < 0 {
				next = pos + 1
			}
			return hostname(strings.Join(labels, ".")), next, nil
		case l&0xc0 == 0xc0:
			if pos+1 >= len(v) || jumps >= maxPointers {
				return "", 0, fmt.Errorf("bad compression pointer")
			}
			target := (l&0x3f)<<8 | int(v[pos+1])
			if target >= pos {
				return "", 0, fmt.Errorf("compression pointer points forward")
			}
			if next < 0 {
				next = pos + 2
			}
			pos, jumps = target, jumps+1
		case l&0xc0 != 0:
			return "", 0, fmt.Errorf("reserved label type")
		default:
			if pos+1+l > len(v) {
				return "", 0, fmt.Errorf("label runs past the option")
			}
			labels = append(labels, string(v[pos+1:pos+1+l]))
			if len(labels) > 127 {
				return "", 0, fmt.Errorf("too many labels")
			}
			pos += 1 + l
		}
	}
}

// clientFQDN decodes option 81 (RFC 4702): flags, two deprecated RCODE
// bytes and the name, in wire format when the E flag is set, else ASCII.
func clientFQDN(v []byte) (*observation.DHCPFQDN, error) {
	if len(v) < 3 {
		return nil, fmt.Errorf("short option")
	}
	fq := &observation.DHCPFQDN{Flags: v[0]}
	if len(v) == 3 {
		return fq, nil
	}
	if v[0]&0x04 != 0 {
		name, _, err := wireName(v[3:], 0)
		if err != nil {
			return nil, err
		}
		fq.Name = name
	} else {
		fq.Name = hostname(string(v[3:]))
	}
	return fq, nil
}

// classlessRoutes decodes option 121 or 249 (RFC 3442): per route a prefix
// width, the significant octets of the destination and the router.
func classlessRoutes(v []byte) ([]observation.DHCPRoute, error) {
	var out []observation.DHCPRoute
	for len(v) > 0 {
		w := int(v[0])
		n := (w + 7) / 8
		if w > 32 || len(v) < 1+n+4 {
			return out, fmt.Errorf("bad route")
		}
		var dst [4]byte
		copy(dst[:], v[1:1+n])
		p := netip.PrefixFrom(netip.AddrFrom4(dst), w)
		if p != p.Masked() {
			return out, fmt.Errorf("route %s has host bits", p)
		}
		if len(out) < maxRoutes {
			out = append(out, observation.DHCPRoute{Destination: p, Router: addr4(v[1+n:])})
		}
		v = v[1+n+4:]
	}
	return out, nil
}

// relayAgent decodes option 82's circuit ID (1) and remote ID (2)
// sub-options (RFC 3046).
func relayAgent(v []byte) (*observation.DHCPRelay, error) {
	ra := &observation.DHCPRelay{}
	for len(v) > 0 {
		if len(v) < 2 || len(v) < 2+int(v[1]) {
			return ra, fmt.Errorf("sub-option runs past the option")
		}
		data := v[2 : 2+int(v[1])]
		switch v[0] {
		case 1:
			ra.CircuitID = hexBytes(data[:min(len(data), maxClientID)])
		case 2:
			ra.RemoteID = hexBytes(data[:min(len(data), maxClientID)])
		}
		v = v[2+int(v[1]):]
	}
	return ra, nil
}

// hexBytes formats b as colon-separated hex, like a MAC.
func hexBytes(b []byte) string {
	var sb strings.Builder
	for i, x := range b {
		if i > 0 {
			sb.WriteByte(':')
		}
		fmt.Fprintf(&sb, "%02x", x)
	}
	return sb.String()
}
