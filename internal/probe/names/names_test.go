package names

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform/fake"
	"github.com/otfabric/lan-tracker/internal/probe"
	"github.com/otfabric/lan-tracker/internal/probe/probetest"
)

var resolver = netip.MustParseAddrPort("192.168.110.2:53")

// answer builds a response to the query q with the given records.
func answer(t *testing.T, q []byte, rcode dnsmessage.RCode, records ...dnsmessage.Resource) []byte {
	t.Helper()
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		t.Fatal(err)
	}
	qs, err := p.AllQuestions()
	if err != nil {
		t.Fatal(err)
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, Authoritative: true, RCode: rcode})
	_ = b.StartQuestions()
	for _, x := range qs {
		_ = b.Question(x)
	}
	_ = b.StartAnswers()
	for _, r := range records {
		switch body := r.Body.(type) {
		case *dnsmessage.AResource:
			_ = b.AResource(r.Header, *body)
		case *dnsmessage.PTRResource:
			_ = b.PTRResource(r.Header, *body)
		case *dnsmessage.CNAMEResource:
			_ = b.CNAMEResource(r.Header, *body)
		}
	}
	if rcode == dnsmessage.RCodeNameError {
		_ = b.StartAuthorities()
		_ = b.SOAResource(dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName("plant.example."), Class: dnsmessage.ClassINET, TTL: 600},
			dnsmessage.SOAResource{NS: dnsmessage.MustNewName("ns.plant.example."), MBox: dnsmessage.MustNewName("h.plant.example."), MinTTL: 900})
	}
	out, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func questionOf(t *testing.T, q []byte) dnsmessage.Question {
	t.Helper()
	var p dnsmessage.Parser
	if _, err := p.Start(q); err != nil {
		t.Fatal(err)
	}
	x, err := p.Question()
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func aRec(name, ip string) dnsmessage.Resource {
	return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: dnsmessage.TypeA,
		Class: dnsmessage.ClassINET, TTL: 120}, Body: &dnsmessage.AResource{A: netip.MustParseAddr(ip).As4()}}
}

func pass(sink *probetest.Sink, qs ...probe.NameQuery) probe.Pass {
	p := probetest.Pass(nil, nil, sink)
	p.Names = qs
	return p
}

// A one-shot mDNS query reads every responder's unicast answer; the
// records come with the responder's address, never a MAC.
func TestMDNSQuery(t *testing.T) {
	tx := &fake.Transmitter{PacketReply: func(to netip.AddrPort, q []byte) []fake.Datagram {
		if to != MDNSGroup || questionOf(t, q).Name.String() != "plc-a.local." {
			t.Errorf("query to %s: %v", to, questionOf(t, q))
		}
		return []fake.Datagram{
			{From: netip.MustParseAddrPort("192.168.110.50:5353"), Data: answer(t, q, 0, aRec("plc-a.local.", "192.168.110.50"))},
			{From: netip.MustParseAddrPort("192.168.110.9:5353"), Data: answer(t, q, 0, aRec("plc-a.local.", "192.168.110.50"))}, // a proxy
			{From: netip.MustParseAddrPort("192.168.110.8:5353"), Data: []byte("junk")},
		}
	}}
	sink := &probetest.Sink{}
	e := &Engine{TX: tx}
	res, err := e.Run(context.Background(), pass(sink, probe.NameQuery{Name: "plc-a.local", Type: uint16(dnsmessage.TypeA), Server: MDNSGroup, MDNS: true}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].State != probe.Reply || res[0].Target != MDNSGroup.Addr() {
		t.Errorf("results = %+v", res)
	}
	obs := sink.All()
	if len(obs) != 2 || obs[0].Source != observation.MDNSQuery || obs[0].MAC != nil || obs[0].IP.String() != "192.168.110.50" ||
		obs[1].IP.String() != "192.168.110.9" || len(obs[0].Records) != 1 || obs[0].Records[0].Addr.String() != "192.168.110.50" {
		t.Errorf("observations = %+v", obs)
	}
	if obs[0].Evidence() != observation.EvidenceNone {
		t.Error("a query answer made its responder present")
	}
}

// Unicast DNS: a PTR answer is a record without MAC; a CNAME-only answer
// is followed, at most twice; a negative answer keeps the question from
// being asked again for its TTL; a timeout records nothing.
func TestDNSQueries(t *testing.T) {
	var asked []string
	tx := &fake.Transmitter{UDPReply: func(to netip.AddrPort, q []byte) []byte {
		if to != resolver {
			t.Errorf("query to %s", to)
		}
		name := questionOf(t, q).Name.String()
		asked = append(asked, name)
		switch name {
		case "50.110.168.192.in-addr.arpa.":
			return answer(t, q, 0, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name),
				Type: dnsmessage.TypePTR, Class: dnsmessage.ClassINET, TTL: 3600}, Body: &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("plc-a.plant.example.")}})
		case "loop.plant.example.", "loop2.plant.example.", "loop3.plant.example.":
			next := map[string]string{"loop.plant.example.": "loop2.plant.example.", "loop2.plant.example.": "loop3.plant.example.",
				"loop3.plant.example.": "loop4.plant.example."}[name]
			return answer(t, q, 0, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name),
				Type: dnsmessage.TypeCNAME, Class: dnsmessage.ClassINET, TTL: 300}, Body: &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(next)}})
		case "gone.plant.example.":
			return answer(t, q, dnsmessage.RCodeNameError)
		}
		return nil // silence
	}}
	sink := &probetest.Sink{}
	e := &Engine{TX: tx}
	q := func(name string, typ dnsmessage.Type) probe.NameQuery {
		return probe.NameQuery{Name: name, Type: uint16(typ), Server: resolver}
	}
	p := pass(sink, q(ReverseName(netip.MustParseAddr("192.168.110.50")), dnsmessage.TypePTR), q("loop.plant.example", dnsmessage.TypeA),
		q("gone.plant.example", dnsmessage.TypeA), q("silent.plant.example", dnsmessage.TypeA))
	res, err := e.Run(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	// PTR, the CNAME chain (the first and two followed), NXDOMAIN, silence.
	if len(res) != 6 || res[5].State != probe.NoReply {
		t.Errorf("results = %+v", res)
	}
	obs := sink.All()
	if len(obs) == 0 || obs[0].Source != observation.DNSQuery || obs[0].IP.IsValid() || obs[0].MAC != nil ||
		obs[0].Records[0].Type != observation.RecordPTR || obs[0].Records[0].Name != "plc-a.plant.example" {
		t.Errorf("observations = %+v", obs)
	}
	asked = nil
	if _, err := e.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	for _, a := range asked {
		if a == "gone.plant.example." {
			t.Error("a negative answer was asked again within its TTL")
		}
	}
}

// Every query takes a start from the budget, whose policy may refuse it.
func TestNameQueriesTakeTheBudget(t *testing.T) {
	sink := &probetest.Sink{}
	p := probetest.Pass(nil, probetest.PolicyFunc(func(_ string, proto probe.Protocol, _ netip.Addr) error {
		if proto != probe.Name {
			t.Errorf("protocol %s", proto)
		}
		return probe.ErrRefused
	}), sink)
	p.Names = []probe.NameQuery{{Name: "x.local", Type: uint16(dnsmessage.TypeA), Server: MDNSGroup, MDNS: true}}
	tx := &fake.Transmitter{}
	res, err := (&Engine{TX: tx}).Run(context.Background(), p)
	if err != nil || len(res) != 1 || res[0].State != probe.Blocked || len(tx.Datagrams()) != 0 {
		t.Errorf("refused: %+v, %v, sent %v", res, err, tx.Datagrams())
	}
	p.Budget = probe.NewBudget(nil, probe.Limits{MaxConcurrent: 1}, probetest.PolicyFunc(func(string, probe.Protocol, netip.Addr) error {
		return probe.ErrDisabled
	}))
	if _, err := (&Engine{TX: tx}).Run(context.Background(), p); !errors.Is(err, probe.ErrDisabled) {
		t.Errorf("kill switch: %v", err)
	}
	if _, err := build(1, probe.NameQuery{Name: "bad..name"}); err == nil {
		t.Error("an invalid name was encoded")
	}
	if (&Engine{}).Protocol() != probe.Name || ReverseName(netip.MustParseAddr("10.1.2.3")) != "3.2.1.10.in-addr.arpa" {
		t.Error("protocol or reverse name")
	}
}
