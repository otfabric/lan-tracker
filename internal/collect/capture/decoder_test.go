package capture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gopacket/gopacket"

	"github.com/otfabric/lan-tracker/internal/collect/capture/decoders"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/test/frames"
)

// fullMDNS is an mDNS response of 64 PTR records whose owner names and
// targets all point back to one 253-character name: about 1.1 KB on the
// wire, about 32 KB of decoded records. i makes the name distinct.
func fullMDNS(i int) []byte {
	first := fmt.Sprintf("%08d", i) + strings.Repeat("a", 53)
	name := first + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 63)
	msg := make([]byte, 12, 1200)
	binary.BigEndian.PutUint16(msg[2:], 0x8400) // response, authoritative
	binary.BigEndian.PutUint16(msg[6:], 64)     // answers
	ptr := []byte{0xc0, 12}                     // the name at offset 12
	rr := func(owner []byte) {
		msg = append(msg, owner...)
		msg = binary.BigEndian.AppendUint16(msg, 12) // PTR
		msg = binary.BigEndian.AppendUint16(msg, 1)  // IN
		msg = binary.BigEndian.AppendUint32(msg, 120)
		msg = binary.BigEndian.AppendUint16(msg, uint16(len(ptr)))
		msg = append(msg, ptr...)
	}
	var wire []byte
	for _, l := range strings.Split(name, ".") {
		wire = append(wire, byte(len(l)))
		wire = append(wire, l...)
	}
	rr(append(wire, 0))
	for range 63 {
		rr(ptr)
	}
	return frames.UDP4(plc, frames.MDNSv4, "192.168.110.50", "224.0.0.251", 5353, 5353, gopacket.Payload(msg))
}

// A full table of the largest mDNS observations stays small: entries are
// fixed-size digests, not the observations' content. The first frames go
// through Decode; decoding 64 records is what makes a frame slow under the
// race detector, so the rest of the table is filled with the keys of the
// same observation under distinct names.
func TestDecoderTableEntriesAreFixedSize(t *testing.T) {
	const decoded = 256
	frame := fullMDNS(0)
	at := bytes.Index(frame, []byte(fmt.Sprintf("%08d", 0))) // the name's distinct part
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	d := NewDecoder(decoders.All)
	var o observation.Observation
	for i := range decoded {
		copy(frame[at:], fmt.Sprintf("%08d", i))
		obs := d.Decode(t0, "eth1", frame)
		if len(obs) != 1 || len(obs[0].Records) != 64 || len(obs[0].Records[0].Name) != 253 {
			t.Fatalf("frame %d: %+v", i, obs)
		}
		o = obs[0]
	}
	for i := decoded; i < maxTracked; i++ {
		o.Records[0].Name = fmt.Sprintf("%08d", i) + o.Records[0].Name[8:]
		d.last[key(o)] = t0
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	if len(d.last) != maxTracked {
		t.Fatalf("table has %d entries, want %d", len(d.last), maxTracked)
	}
	if grown := int64(after.HeapAlloc) - int64(before.HeapAlloc); grown > 8<<20 {
		t.Errorf("table of %d entries retains %d bytes", maxTracked, grown)
	}
	runtime.KeepAlive(d)
}

// Observations share a key only when their content is identical: any
// field, a record's TTL or a DHCP message's stage tells them apart, and a
// separator inside a value cannot make two different observations equal.
func TestDecoderKeyIsContent(t *testing.T) {
	base := func() observation.Observation {
		return observation.Observation{
			Time: t0, Interface: "eth1", Source: observation.PassiveMDNS, MAC: plc,
			IP: netip.MustParseAddr("192.168.110.50"), NameType: "mdns", Hostname: "plc-1",
			Meta: map[string]string{"b": "2", "a": "1"},
			Records: []observation.Record{{Type: observation.RecordA, Name: "plc-1.local",
				Addr: netip.MustParseAddr("192.168.110.50"), TTL: observation.TTLOf(120)}},
			DHCP: &observation.DHCPMessage{Op: 1, Type: "DISCOVER", XID: 7},
		}
	}
	same := base()
	same.Time = t0.Add(time.Hour) // time is not content
	same.Meta = map[string]string{"a": "1", "b": "2"}
	if key(base()) != key(same) {
		t.Error("identical content has different keys")
	}
	for name, change := range map[string]func(*observation.Observation){
		"interface": func(o *observation.Observation) { o.Interface = "eth2" },
		"no ip":     func(o *observation.Observation) { o.IP = netip.Addr{} },
		"hostname":  func(o *observation.Observation) { o.Hostname = "plc-2" },
		"meta":      func(o *observation.Observation) { o.Meta["a"] = "3" },
		"no meta":   func(o *observation.Observation) { o.Meta = nil },
		"ttl":       func(o *observation.Observation) { o.Records[0].TTL = observation.TTLOf(0) },
		"stage":     func(o *observation.Observation) { o.DHCP.Type = "REQUEST" },
		"no dhcp":   func(o *observation.Observation) { o.DHCP = nil },
		"separator": func(o *observation.Observation) { o.NameType, o.Hostname = "mdns:plc", "1" },
	} {
		o := base()
		change(&o)
		if key(o) == key(base()) {
			t.Errorf("%s: different content has the same key", name)
		}
	}

	// Forget lets the next identical frame through again.
	d := NewDecoder(decoders.All)
	frame := fullMDNS(1)
	got := d.Decode(t0, "eth1", frame)
	if len(got) != 1 || len(d.Decode(t0.Add(time.Second), "eth1", frame)) != 0 {
		t.Fatal("repeat not suppressed")
	}
	d.Forget(got[0])
	if n := len(d.Decode(t0.Add(2*time.Second), "eth1", frame)); n != 1 {
		t.Errorf("after Forget: %d observations", n)
	}
}
