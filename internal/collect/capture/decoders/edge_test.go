package decoders

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/gopacket/gopacket/layers"

	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/test/frames"
)

// tlv encodes one LLDP TLV.
func tlv(typ byte, v ...byte) []byte {
	return append([]byte{typ<<1 | byte(len(v)>>8), byte(len(v))}, v...)
}

func TestLLDPIdentifiers(t *testing.T) {
	ttl := tlv(lldpTTL, 0, 120)
	tests := []struct {
		name string
		pdu  [][]byte
		want string
	}{
		{"network-address chassis, MAC port, IPv6 management address",
			[][]byte{
				tlv(lldpChassisID, lldpChassisNetwork, 1, 192, 168, 110, 2),
				tlv(lldpPortID, lldpPortMAC, 0x00, 0x1e, 0xc9, 0, 0, 0x17),
				ttl,
				tlv(lldpMgmtAddress, append([]byte{17, 2}, frames.IP("fd00::2")...)...),
				tlv(lldpMgmtAddress, 5, 1, 10, 0, 0, 2), // a second address is ignored
				tlv(lldpEnd),
				tlv(lldpSystemName, 'x'), // after the end TLV
			},
			"passive_lldp mac=00:00:00:00:00:01 chassis_id=192.168.110.2 management_ip=fd00::2 port_id=00:1e:c9:00:00:17"},
		{"network-address port id, bad management address",
			[][]byte{
				tlv(lldpChassisID, 7, 'S', 'N', '1'),
				tlv(lldpPortID, lldpPortNetwork, 1, 10, 0, 0, 9),
				ttl,
				tlv(lldpMgmtAddress, 9, 1, 10, 0),
				tlv(lldpSysCaps, 0, 0xff),
			},
			"passive_lldp mac=00:00:00:00:00:01 chassis_id=SN1 port_id=10.0.0.9"},
		{"a chassis MAC is metadata: the frame source is the attachment",
			[][]byte{tlv(lldpChassisID, lldpChassisMAC, 0x01, 0, 0x5e, 0, 0, 1), tlv(lldpPortID, 5, 'p'), ttl},
			"passive_lldp mac=00:00:00:00:00:01 chassis_id=01:00:5e:00:00:01 port_id=p"},
		{"short chassis id", [][]byte{tlv(lldpChassisID, 4), ttl}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &frame{p: All, time: t0, iface: "eth1", src: frames.MAC("00:00:00:00:00:01")}
			f.lldp(slices.Concat(tt.pdu...))
			got := strings.Join(summaries(f.out), "|")
			if got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

// rawDHCP is a BOOTREPLY ACK for hmi with the given options field, sname
// and file.
func rawDHCP(opts, sname, file []byte) []byte {
	b := make([]byte, dhcpFixedLen)
	b[0], b[1], b[2] = 2, 1, 6
	copy(b[28:], hmi)
	copy(b[44:108], sname)
	copy(b[108:236], file)
	binary.BigEndian.PutUint32(b[236:], dhcpMagic)
	return append(append(b, dhcpOptMessageType, 1, 5), opts...)
}

func TestDHCPGather(t *testing.T) {
	opts, present, truncated := dhcpGather(rawDHCP([]byte{
		dhcpOptPad, dhcpOptPad,
		dhcpOptHostname, 3, 'p', 'l', 'c',
		dhcpOptHostname, 2, '-', '1', // concatenated (RFC 3396)
		dhcpOptClientID, 9, 1, 2, // truncated: parsing stops
		dhcpOptVendorClass, 1, 'x',
	}, nil, nil))
	if string(opts[dhcpOptHostname]) != "plc-1" || opts[dhcpOptClientID] != nil || opts[dhcpOptVendorClass] != nil || !truncated ||
		!slices.Equal(present, []int{dhcpOptHostname, dhcpOptMessageType}) {
		t.Errorf("options = %q, present %v, truncated %v", opts, present, truncated)
	}
	if _, _, truncated := dhcpGather(rawDHCP([]byte{dhcpOptHostname, 1, 'a'}, nil, nil)); !truncated {
		t.Error("an options field without the end option is complete")
	}
	opts, _, truncated = dhcpGather(rawDHCP([]byte{dhcpOptHostname, 1, 'a', dhcpOptEnd, dhcpOptVendorClass, 1, 'b'}, nil, nil))
	if opts[dhcpOptVendorClass] != nil || truncated {
		t.Error("options after the end option were read")
	}
	// Overload: the file field, then sname, each joined to the options.
	opts, _, _ = dhcpGather(rawDHCP([]byte{dhcpOptDomain, 1, 'a', dhcpOptOverload, 1, 3, dhcpOptEnd},
		[]byte{dhcpOptDomain, 2, '.', 'y', dhcpOptEnd}, []byte{dhcpOptDomain, 2, '.', 'x', dhcpOptEnd}))
	if string(opts[dhcpOptDomain]) != "a.x.y" {
		t.Errorf("overloaded option = %q", opts[dhcpOptDomain])
	}
	if opts, _, _ := dhcpGather(rawDHCP([]byte{dhcpOptDomain, 1, 'a', dhcpOptEnd}, []byte{dhcpOptDomain, 1, 'z'}, nil)); string(opts[dhcpOptDomain]) != "a" {
		t.Error("sname read without option 52")
	}
	var long []byte
	for range 5 {
		long = append(long, append([]byte{dhcpOptDomain, 255}, make([]byte, 255)...)...)
	}
	if opts, _, _ := dhcpGather(rawDHCP(append(long, dhcpOptEnd), nil, nil)); len(opts[dhcpOptDomain]) != maxOptionBytes {
		t.Errorf("joined option of %d bytes", len(opts[dhcpOptDomain]))
	}

	// The op code must agree with the message type.
	b := frames.DHCP{Type: layers.DHCPMsgTypeAck, Client: hmi, YourIP: "10.0.0.5", Server: server, ServerIP: "10.0.0.2"}.Frame()[14+20+8:]
	b = slices.Clone(b)
	b[0] = 1 // BOOTREQUEST carrying an ACK
	f := &frame{p: All, time: t0, iface: "eth1"}
	f.dhcp(netip.MustParseAddr("10.0.0.2"), b)
	if len(f.out) != 0 {
		t.Errorf("mismatched op accepted: %v", summaries(f.out))
	}
	b[0] = 3 // neither request nor reply
	f.dhcp(netip.MustParseAddr("10.0.0.2"), b)
	d := frames.DHCP{Type: layers.DHCPMsgTypeDiscover, Client: hmi}.Frame()[14+20+8:]
	d = slices.Clone(d)
	d[0] = 3
	f.dhcp(netip.Addr{}, d)
	if len(f.out) != 0 {
		t.Errorf("unknown op accepted: %v", summaries(f.out))
	}
	// A reply in a frame without a unicast source has no sender to report.
	f = &frame{p: All, time: t0, iface: "eth1"}
	f.dhcp(netip.MustParseAddr("10.0.0.2"), frames.DHCP{Type: layers.DHCPMsgTypeOffer, Client: hmi, Server: server, ServerIP: "10.0.0.2"}.Frame()[14+20+8:])
	if len(f.out) != 0 {
		t.Errorf("reply without a sender MAC = %v", summaries(f.out))
	}
	// A truncated options field is reported, with what it held.
	m, ok := parseDHCP(rawDHCP([]byte{dhcpOptRouter, 4, 10, 0, 0, 1, dhcpOptDNS, 8, 1}, nil, nil))
	if !ok || !m.Truncated || len(m.Routers) != 1 || m.DNS != nil {
		t.Errorf("truncated message = %+v", m)
	}
	// BOOTP without option 53, and a non-Ethernet client.
	if _, ok := parseDHCP(rawDHCP(nil, nil, nil)[:dhcpFixedLen]); ok {
		t.Error("BOOTP decoded as DHCP")
	}
	nonEth := rawDHCP([]byte{dhcpOptEnd}, nil, nil)
	nonEth[1] = 6 // IEEE 802
	if m, ok := parseDHCP(nonEth); !ok || m.CHAddr != "" {
		t.Errorf("non-Ethernet chaddr = %+v", m)
	}
}

func TestDHCPOptionValues(t *testing.T) {
	parse := func(opts ...byte) *observation.DHCPMessage {
		m, ok := parseDHCP(rawDHCP(append(opts, dhcpOptEnd), nil, nil))
		if !ok {
			t.Fatalf("options %v not decoded", opts)
		}
		return m
	}
	if m := parse(dhcpOptRouter, 8, 10, 0, 0, 1, 10, 0, 0, 2); len(m.Routers) != 2 || len(m.Malformed) != 0 {
		t.Errorf("address list = %+v", m)
	}
	if m := parse(dhcpOptRouter, 5, 10, 0, 0, 1, 10); m.Routers != nil || !slices.Equal(m.Malformed, []int{dhcpOptRouter}) {
		t.Errorf("address list not a multiple of 4 = %+v", m)
	}
	if m := parse(dhcpOptDNS, 0); !slices.Equal(m.Malformed, []int{dhcpOptDNS}) {
		t.Errorf("empty address list = %+v", m)
	}
	if m := parse(append([]byte{dhcpOptDNS, 4 * (maxAddrList + 3)}, make([]byte, 4*(maxAddrList+3))...)...); len(m.DNS) != maxAddrList {
		t.Errorf("long address list kept %d", len(m.DNS))
	}
	if m := parse(dhcpOptServerID, 3, 10, 0, 1); m.ServerID.IsValid() || !slices.Equal(m.Malformed, []int{dhcpOptServerID}) {
		t.Errorf("short server identifier = %+v", m)
	}
	for _, id := range [][]byte{{0, 0, 0, 0}, {224, 0, 0, 1}} {
		if m := parse(append([]byte{dhcpOptServerID, 4}, id...)...); m.ServerID.IsValid() {
			t.Errorf("server identifier %v accepted", id)
		}
	}
	if m := parse(dhcpOptRelayAgent, 6, 1, 2, 'p', '7', 2, 9); m.RelayAgent != nil || !slices.Equal(m.Malformed, []int{dhcpOptRelayAgent}) {
		t.Errorf("truncated relay agent sub-option = %+v", m)
	}
	if m := parse(dhcpOptFQDN, 2, 0, 0); m.FQDN != nil || !slices.Equal(m.Malformed, []int{dhcpOptFQDN}) {
		t.Errorf("short FQDN = %+v", m)
	}
	if m := parse(append([]byte{dhcpOptFQDN, 7, 0x01, 0, 0}, "hmi."...)...); m.FQDN == nil || m.FQDN.Name != "hmi" || m.FQDN.Flags != 1 {
		t.Errorf("ASCII FQDN = %+v", m.FQDN)
	}
	if m := parse(dhcpOptFQDN, 3, 0x02, 0, 0); m.FQDN == nil || m.FQDN.Name != "" {
		t.Errorf("FQDN flags only = %+v", m.FQDN)
	}
	if m := parse(dhcpOptDomain, 1, 0x01); m.Domain != "" || !slices.Equal(m.Malformed, []int{dhcpOptDomain}) {
		t.Errorf("unprintable domain = %+v", m)
	}
	for name, v := range map[string][]byte{
		"forward pointer":   {0xc0, 2, 0},
		"pointer to itself": {1, 'a', 0xc0, 0},
		"reserved label":    {0x40, 0},
		"label past option": {5, 'a'},
		"no root label":     {1, 'a'},
	} {
		if m := parse(append([]byte{dhcpOptSearch, byte(len(v))}, v...)...); !slices.Equal(m.Malformed, []int{dhcpOptSearch}) {
			t.Errorf("search list with %s = %+v", name, m)
		}
	}
	for name, v := range map[string][]byte{
		"host bits":   {20, 10, 1, 0xff, 10, 0, 0, 1},
		"width > 32":  {33, 10, 1, 1, 1, 1, 10, 0, 0, 1},
		"short route": {8, 10, 10, 0},
	} {
		if m := parse(append([]byte{dhcpOptRoutes, byte(len(v))}, v...)...); !slices.Equal(m.Malformed, []int{dhcpOptRoutes}) {
			t.Errorf("classless routes with %s = %+v", name, m)
		}
	}
	// Option 249 is decoded like 121 but sets no gateway.
	if m := parse(dhcpOptMSRoutes, 5, 0, 10, 0, 0, 1); len(m.MSRoutes) != 1 || m.Routes != nil {
		t.Errorf("option 249 = %+v", m)
	} else if _, from := m.Gateway(); from != "" {
		t.Errorf("option 249 gave a gateway from %q", from)
	}
}

func TestNDPOptions(t *testing.T) {
	mac := frames.MAC("00:1b:1b:aa:bb:01")
	nonce := []byte{14, 1, 1, 2, 3, 4, 5, 6} // RFC 3971 nonce option, skipped
	slla := append([]byte{ndpOptSourceLL, 1}, mac...)
	if got := linkLayerOption(slices.Concat(nonce, slla), ndpOptSourceLL); got.String() != mac.String() {
		t.Errorf("option after a nonce = %v", got)
	}
	if got := linkLayerOption(slices.Concat([]byte{14, 0, 0, 0, 0, 0, 0, 0}, slla), ndpOptSourceLL); got != nil {
		t.Errorf("zero-length option not rejected: %v", got)
	}
	if got := linkLayerOption([]byte{ndpOptSourceLL, 2, 1, 2, 3}, ndpOptSourceLL); got != nil {
		t.Errorf("option longer than the message accepted: %v", got)
	}
	if got := linkLayerOption(slla, ndpOptTargetLL); got != nil {
		t.Errorf("wrong option type matched: %v", got)
	}
	// Router advertisements and solicitations shorter than their fixed part
	// are ignored, and so are unknown NDP types (redirect).
	for _, b := range [][]byte{{134, 0, 0, 0, 0, 0, 0, 0}, {135, 0, 0, 0, 0, 0, 0, 0}, {136, 0, 0, 0, 0, 0, 0, 0}, {137, 0, 0, 0, 0, 0, 0, 0}} {
		f := newFrame()
		f.ndp(addr16(frames.IP("fe80::1")), 255, b)
		if len(f.out) != 0 {
			t.Errorf("type %d: %v", b[0], summaries(f.out))
		}
	}
}

// One mDNS message contributes at most maxRecords records.
func TestMDNSRecordLimit(t *testing.T) {
	var records []layers.DNSResourceRecord
	for i := range maxRecords + 10 {
		records = append(records, frames.PTR(fmt.Sprintf("_s%d._tcp.local", i), fmt.Sprintf("x._s%d._tcp.local", i)))
	}
	f := newFrame()
	f.mdns(addr4(frames.IP("10.0.0.5").To4()), frames.MDNS4(plc, "10.0.0.5", records, nil)[14+20+8:])
	if len(f.out) != 1 || len(f.out[0].Records) != maxRecords {
		t.Fatalf("observations = %v", summaries(f.out))
	}
}

// IPv6 packets with extension headers (MLD hop-by-hop) only teach the
// source address; truncated UDP headers teach nothing more.
func TestIPv6ExtensionHeadersAndShortUDP(t *testing.T) {
	ip6 := frames.Build(frames.Ethernet(plc, frames.MAC("33:33:00:00:00:16"), layers.EthernetTypeIPv6),
		frames.IPv6("fe80::21b:1bff:feaa:bb01", "ff02::16", layers.IPProtocolIPv6HopByHop, 1))
	if got := summaries(Decode(All, t0, "eth1", ip6)); len(got) != 1 || !strings.HasPrefix(got[0], "passive_ipv6") {
		t.Errorf("hop-by-hop: %v", got)
	}
	f := newFrame()
	f.udp(addr4(frames.IP("10.0.0.5").To4()), []byte{0, 53, 0, 1})
	if len(f.out) != 0 {
		t.Errorf("short UDP: %v", summaries(f.out))
	}
}

// Records of types the decoder does not read (here URI; in the field NSEC,
// HINFO or OPT) are skipped in every section, so addresses after them are
// still read.
func TestMDNSSkipsUnknownRecords(t *testing.T) {
	other := layers.DNSResourceRecord{Name: []byte("plc-a.local"), Type: layers.DNSTypeURI, Class: layers.DNSClassIN, TTL: 120,
		URI: layers.DNSURI{Priority: 1, Weight: 1, Target: []byte("http://plc-a.local/")}}
	b := frames.MDNS4(plc, "10.0.0.5",
		[]layers.DNSResourceRecord{other, frames.PTR("_http._tcp.local", "x._http._tcp.local")},
		[]layers.DNSResourceRecord{other, frames.A("plc-a.local", "10.0.0.5")})
	f := newFrame()
	f.mdns(addr4(frames.IP("10.0.0.5").To4()), b[14+20+8:])
	if got := strings.Join(summaries(f.out), "|"); got != "passive_mdns mac=00:1b:1b:aa:bb:01 ip=10.0.0.5 rec=PTR:_http._tcp.local>x._http._tcp.local/120 rec=A:plc-a.local>10.0.0.5/120" {
		t.Errorf("got %s", got)
	}
}
