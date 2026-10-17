package decoders

import (
	"net/netip"
	"testing"

	"github.com/gopacket/gopacket/layers"

	"github.com/otfabric/lan-tracker/test/frames"
)

// Every decoder has a fuzz target (AGENTS.md §7). Each feeds a
// protocol payload to its decoder with every protocol enabled and checks the
// invariants every observation must meet. make fuzz runs them all.

func checkFrame(t *testing.T, f *frame) {
	t.Helper()
	for _, o := range f.out {
		if o.MAC == nil && !o.IP.IsValid() && len(o.Records) == 0 {
			t.Fatalf("observation without evidence: %+v", o)
		}
		if o.Hostname == "" && o.NameType != "" || len(o.Hostname) > maxName {
			t.Fatalf("bad name: %+v", o)
		}
		if len(o.Records) > maxRecords {
			t.Fatalf("%d records", len(o.Records))
		}
		for _, r := range o.Records {
			if r.Name == "" || len(r.Name) > maxName || len(r.Value) > maxName+6 && len(r.Value) > maxText || r.TTL == nil ||
				r.Addr.IsValid() && !usableIP(r.Addr) {
				t.Fatalf("bad record: %+v", r)
			}
		}
		if !o.Time.Equal(t0) || o.Interface != "eth1" {
			t.Fatalf("time or interface not set: %+v", o)
		}
		if m := o.DHCP; m != nil && (len(m.Routers) > maxAddrList || len(m.DNS) > maxAddrList || len(m.Routes) > maxRoutes ||
			len(m.MSRoutes) > maxRoutes || len(m.Search) > maxSearch || len(m.ParamRequest) > maxParamRequest ||
			len(m.ClientID) > 3*maxClientID || len(m.Hostname) > maxName || len(m.Domain) > maxName) {
			t.Fatalf("unbounded DHCP message: %+v", m)
		}
	}
}

func newFrame() *frame {
	return &frame{p: All, time: t0, iface: "eth1", src: copyMAC(plc)}
}

func FuzzDecode(f *testing.F) {
	for _, b := range seedFrames() {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		fr := &frame{p: All, time: t0, iface: "eth1"}
		fr.out = Decode(All, t0, "eth1", b)
		checkFrame(t, fr)
	})
}

func FuzzARP(f *testing.F) {
	f.Add(frames.ARP(layers.ARPReply, plc, "192.168.110.50", hmi, "192.168.110.20")[14:])
	f.Add(frames.ARP(layers.ARPRequest, plc, "0.0.0.0", nobody, "192.168.110.52")[14:])
	f.Fuzz(func(t *testing.T, b []byte) {
		fr := newFrame()
		fr.arp(b)
		checkFrame(t, fr)
	})
}

func FuzzIPv4(f *testing.F) {
	f.Add(frames.ICMPv4Echo(hmi, plc, "192.168.110.20", "192.168.110.50")[14:])
	f.Add(ackFrame()[14:])
	f.Fuzz(func(t *testing.T, b []byte) {
		fr := newFrame()
		fr.ipv4(b)
		checkFrame(t, fr)
	})
}

func FuzzIPv6(f *testing.F) {
	f.Add(frames.NeighborAdvertisement(plc, "fe80::21b:1bff:feaa:bb01", "fd5e:5e:1::50", 0xa0)[14:])
	f.Fuzz(func(t *testing.T, b []byte) {
		fr := newFrame()
		fr.ipv6(b)
		checkFrame(t, fr)
	})
}

func FuzzNDP(f *testing.F) {
	for _, b := range [][]byte{
		frames.NeighborAdvertisement(plc, "fe80::21b:1bff:feaa:bb01", "fd5e:5e:1::50", 0xa0),
		frames.NeighborSolicitation(hmi, "::", "fd5e:5e:1::20"),
		frames.RouterAdvertisement(router, "fe80::1"),
	} {
		f.Add(b[14+40:])
	}
	src := addr16(frames.IP("fe80::1"))
	f.Fuzz(func(t *testing.T, b []byte) {
		fr := newFrame()
		fr.ndp(src, 255, b)
		checkFrame(t, fr)
	})
}

func FuzzDHCP(f *testing.F) {
	f.Add(frames.DHCP{Type: layers.DHCPMsgTypeDiscover, Client: hmi, Hostname: "HMI-Line3",
		ClientID: []byte{1, 2, 3}, ParamRequest: []byte{1, 3, 6}, VendorClass: "MSFT 5.0"}.Frame()[14+20+8:])
	f.Add(ackFrame()[14+20+8:])
	f.Add(frames.DHCP{Type: layers.DHCPMsgTypeOffer, Client: hmi, YourIP: "192.168.110.21", Server: router, ServerIP: "192.168.110.1",
		ServerID: "10.1.0.5", Relay: "192.168.110.1", RelayAgent: []byte{1, 2, 'p', '7'}, Router: []string{"192.168.110.1"},
		DNS: []string{"10.1.0.5"}, SubnetMask: "255.255.255.0", LeaseSeconds: 3600}.Frame()[14+20+8:])
	f.Fuzz(func(t *testing.T, b []byte) {
		fr := newFrame()
		fr.dhcp(netip.MustParseAddr("192.168.110.2"), b)
		checkFrame(t, fr)
	})
}

func FuzzMDNS(f *testing.F) {
	f.Add(frames.MDNS4(plc, "192.168.110.50", []layers.DNSResourceRecord{
		frames.PTR("_http._tcp.local", "PLC 1._http._tcp.local"),
		frames.SRV("PLC 1._http._tcp.local", "plc-1.local", 80),
		frames.TXT("PLC 1._http._tcp.local", "model=S7-1500"),
		frames.A("plc-1.local", "192.168.110.50"),
	}, nil)[14+20+8:])
	src := addr4(frames.IP("192.168.110.50").To4())
	f.Fuzz(func(t *testing.T, b []byte) {
		fr := newFrame()
		fr.mdns(src, b)
		checkFrame(t, fr)
	})
}

func FuzzDNS(f *testing.F) {
	f.Add(frames.UDP4(server, hmi, "192.168.110.2", "192.168.110.20", 53, 40000, frames.DNSResponse(
		[]layers.DNSResourceRecord{frames.PTR("50.110.168.192.in-addr.arpa", "plc-1.plant.example.")}, nil))[14+20+8:])
	f.Fuzz(func(t *testing.T, b []byte) {
		fr := newFrame()
		fr.dns(b)
		checkFrame(t, fr)
	})
}

func FuzzLLDP(f *testing.F) {
	f.Add(frames.LLDP{Source: swPort, ChassisMAC: sw, PortID: "ge-0/0/23", TTL: 120, SystemName: "plant-sw-01",
		SystemDesc: "switch", PortDesc: "PLC", Caps: 0x14, MgmtIP: "192.168.110.2"}.Frame()[14:])
	f.Fuzz(func(t *testing.T, b []byte) {
		fr := newFrame()
		fr.lldp(b)
		checkFrame(t, fr)
	})
}

func ackFrame() []byte {
	return frames.DHCP{Type: layers.DHCPMsgTypeAck, Client: hmi, YourIP: "192.168.110.21", Server: server, ServerIP: "192.168.110.2"}.Frame()
}

// seedFrames is one frame of every kind.
func seedFrames() [][]byte {
	return [][]byte{
		frames.ARP(layers.ARPReply, plc, "192.168.110.50", hmi, "192.168.110.20"),
		frames.ICMPv4Echo(hmi, plc, "192.168.110.20", "192.168.110.50"),
		ackFrame(),
		frames.MDNS4(plc, "192.168.110.50", []layers.DNSResourceRecord{frames.A("plc-1.local", "192.168.110.50")}, nil),
		frames.NeighborAdvertisement(plc, "fe80::21b:1bff:feaa:bb01", "fd5e:5e:1::50", 0xa0),
		frames.LLDP{Source: swPort, ChassisMAC: sw, PortID: "1", TTL: 120, SystemName: "sw"}.Frame(),
	}
}
