package names

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/otfabric/lan-tracker/internal/platform/fake"
	"github.com/otfabric/lan-tracker/internal/probe"
	"github.com/otfabric/lan-tracker/internal/probe/probetest"
)

func cnameRec(name, target string) dnsmessage.Resource {
	return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: dnsmessage.TypeCNAME,
		Class: dnsmessage.ClassINET, TTL: 300}, Body: &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName(target)}}
}

// chains answers "<x>.plant.example" with a CNAME to "<x>-b", that with a
// CNAME to "<x>-c", and that with an address; "<x>-nx" does not exist and
// "<x>-quiet" never answers. asked counts the queries per name.
func chains(t *testing.T, asked map[string]int) *fake.Transmitter {
	return &fake.Transmitter{UDPReply: func(_ netip.AddrPort, q []byte) []byte {
		name := questionOf(t, q).Name.String()
		asked[name]++
		host, _, _ := strings.Cut(name, ".")
		switch {
		case strings.HasPrefix(host, "to-"):
			return answer(t, q, 0, cnameRec(name, strings.TrimPrefix(host, "to")+".plant.example."))
		case strings.HasSuffix(host, "-nx"):
			return answer(t, q, dnsmessage.RCodeNameError)
		case strings.HasSuffix(host, "-quiet"):
			return nil
		case strings.HasSuffix(host, "-c"):
			return answer(t, q, 0, aRec(name, "192.168.110.50"))
		case strings.HasSuffix(host, "-b"):
			return answer(t, q, 0, cnameRec(name, strings.TrimSuffix(host, "-b")+"-c.plant.example."))
		}
		return answer(t, q, 0, cnameRec(name, host+"-b.plant.example."))
	}}
}

// Every query sent counts against the pass's cap, CNAME follow-ups
// included: five questions answered with CNAME chains and a cap of five
// send five queries, and the pass stops cut short. The question whose
// chain the cap left unresolved is kept as negative, so the next pass
// does not spend its queries on it again.
func TestNamePassCountsFollowUpsAgainstItsCap(t *testing.T) {
	asked := map[string]int{}
	tx := chains(t, asked)
	e := &Engine{TX: tx}
	var qs []probe.NameQuery
	for _, h := range []string{"a1", "a2", "a3", "a4", "a5"} {
		qs = append(qs, probe.NameQuery{Name: h + ".plant.example", Type: uint16(dnsmessage.TypeA), Server: resolver})
	}
	p := pass(&probetest.Sink{}, qs...)
	p.MaxQueries = 5
	res, err := e.Run(context.Background(), p)
	if !errors.Is(err, probe.ErrPassCap) {
		t.Fatalf("err = %v", err)
	}
	if n := len(tx.Datagrams()); n != 5 || len(res) != 5 {
		t.Fatalf("sent %d queries (%d results), want 5", n, len(res))
	}
	// a1 resolved through two CNAMEs (3 queries); a2's chain was cut.
	if asked["a1-c.plant.example."] != 1 || asked["a2-b.plant.example."] != 1 || asked["a2-c.plant.example."] != 0 {
		t.Errorf("asked = %v", asked)
	}
	if _, err := e.Run(context.Background(), p); !errors.Is(err, probe.ErrPassCap) {
		t.Fatalf("second pass: %v", err)
	}
	if n := len(tx.Datagrams()); n != 10 {
		t.Errorf("sent %d queries over two passes, want 10", n)
	}
	if asked["a2.plant.example."] != 1 || asked["a1.plant.example."] != 2 || asked["a3.plant.example."] != 1 {
		t.Errorf("second pass asked = %v", asked)
	}

	// Without a cap every chain is followed.
	p.MaxQueries = 0
	if _, err := (&Engine{TX: chains(t, map[string]int{})}).Run(context.Background(), p); err != nil {
		t.Errorf("uncapped pass: %v", err)
	}
}

// A CNAME-only answer whose target does not exist, or whose chain is
// longer than the engine follows, is kept as negative; one whose target
// stays silent is asked again.
func TestCNAMEToUnresolvableTarget(t *testing.T) {
	asked := map[string]int{}
	e := &Engine{TX: chains(t, asked)}
	q := func(name string) probe.NameQuery {
		return probe.NameQuery{Name: name, Type: uint16(dnsmessage.TypeA), Server: resolver}
	}
	p := pass(&probetest.Sink{}, q("to-nx.plant.example"), q("to-quiet.plant.example"))
	for range 2 {
		if _, err := e.Run(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	if asked["to-nx.plant.example."] != 1 || asked["-nx.plant.example."] != 1 || asked["to-quiet.plant.example."] != 2 {
		t.Errorf("asked = %v", asked)
	}
	if !e.negativeUntil(question(q("to-nx.plant.example"))).Equal(e.negativeUntil(question(q("-nx.plant.example")))) {
		t.Error("the CNAME's negative answer does not last as long as its target's")
	}
}
