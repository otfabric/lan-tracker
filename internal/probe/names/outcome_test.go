package names

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/platform/fake"
	"github.com/otfabric/lan-tracker/internal/probe"
	"github.com/otfabric/lan-tracker/internal/probe/probetest"
)

// noSockets is a transmitter whose UDP sockets cannot be opened (no route,
// a missing interface).
type noSockets struct{ *fake.Transmitter }

var errNoSocket = errors.New("network is unreachable")

func (noSockets) DialUDP(context.Context, string, netip.AddrPort) (net.Conn, error) {
	return nil, errNoSocket
}
func (noSockets) ListenUDP(context.Context, string) (net.PacketConn, error) { return nil, errNoSocket }

// A socket that cannot be opened, or a name that cannot be encoded, stops
// the pass with the interface in the error; nothing is sent.
func TestNameQueryLocalFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		tx   *fake.Transmitter
		q    probe.NameQuery
		want string
	}{
		{"dns socket", nil, probe.NameQuery{Name: "plc-a.plant.example", Type: uint16(dnsmessage.TypeA), Server: resolver}, "names on eth1"},
		{"mdns socket", nil, probe.NameQuery{Name: "plc-a.local", Type: uint16(dnsmessage.TypeA), Server: MDNSGroup, MDNS: true}, "names on eth1"},
		{"name too long", &fake.Transmitter{}, probe.NameQuery{Name: strings.Repeat("plc-a.", 50) + "local", Type: uint16(dnsmessage.TypeA), Server: resolver}, "query"},
		{"label too long", &fake.Transmitter{}, probe.NameQuery{Name: strings.Repeat("x", 64) + ".local", Type: uint16(dnsmessage.TypeA), Server: resolver}, `query "` + strings.Repeat("x", 64) + `.local": `},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var tx platform.Transmitter = noSockets{&fake.Transmitter{}}
			if tt.tx != nil {
				tx = tt.tx
			}
			_, err := (&Engine{TX: tx}).Run(context.Background(), pass(&probetest.Sink{}, tt.q))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v", err)
			}
			if tt.tx != nil && len(tt.tx.Datagrams()) != 0 {
				t.Errorf("sent %v", tt.tx.Datagrams())
			}
		})
	}
}

// What a resolver answers decides the outcome: an answer of another query
// (wrong ID) is not a reply; an answer with the asked record is not
// followed even beside a CNAME; a server failure is not remembered as a
// negative answer, so the question is asked again.
func TestDNSOutcomes(t *testing.T) {
	asked := map[string]int{}
	tx := &fake.Transmitter{UDPReply: func(_ netip.AddrPort, q []byte) []byte {
		name := questionOf(t, q).Name.String()
		asked[name]++
		switch name {
		case "other.plant.example.":
			b := answer(t, q, 0, aRec(name, "192.168.110.60"))
			binary.BigEndian.PutUint16(b, binary.BigEndian.Uint16(b)+1)
			return b
		case "both.plant.example.":
			return answer(t, q, 0, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name),
				Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 300}, Body: &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("plc-a.plant.example.")}},
				aRec(name, "192.168.110.50"))
		case "broken.plant.example.":
			return answer(t, q, dnsmessage.RCodeServerFailure)
		}
		return nil
	}}
	e := &Engine{TX: tx}
	q := func(name string) probe.NameQuery {
		return probe.NameQuery{Name: name, Type: uint16(dnsmessage.TypeA), Server: resolver}
	}
	p := pass(&probetest.Sink{}, q("other.plant.example"), q("both.plant.example"), q("broken.plant.example"))
	for range 2 {
		res, err := e.Run(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		if len(res) != 3 || res[0].State != probe.NoReply || res[1].State != probe.Reply || res[2].State != probe.Reply {
			t.Errorf("results = %+v", res)
		}
	}
	if asked["plc-a.plant.example."] != 0 || asked["broken.plant.example."] != 2 {
		t.Errorf("asked = %v", asked)
	}
}
