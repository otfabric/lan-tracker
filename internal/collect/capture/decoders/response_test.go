package decoders

import (
	"net/netip"
	"strings"
	"testing"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/otfabric/lan-tracker/internal/observation"
)

// response builds a DNS response with the given answers and authorities.
func response(t *testing.T, answers, authorities []dnsmessage.Resource) []byte {
	t.Helper()
	m := dnsmessage.Message{Header: dnsmessage.Header{ID: 7, Response: true}, Answers: answers, Authorities: authorities,
		Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("plc-a.local."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	b, err := m.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func rr(name string, class dnsmessage.Class, body dnsmessage.ResourceBody) dnsmessage.Resource {
	return dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Class: class, TTL: 120}, Body: body}
}

func record(r observation.Record) string {
	s := string(r.Type) + ":" + r.Name + ">" + r.Value
	if r.Addr.IsValid() {
		s += r.Addr.String()
	}
	if r.Flush {
		s += " flush"
	}
	return s
}

// TestParseResponse reads the answers a name query gets back (the names
// probe): every record type it keeps, the SOA minimum of a negative
// answer, and what a malformed or cut-off response still yields.
func TestParseResponse(t *testing.T) {
	in := dnsmessage.ClassINET
	full := []dnsmessage.Resource{
		rr("plc-a.local.", in|cacheFlush, &dnsmessage.AResource{A: [4]byte{10, 0, 0, 5}}),
		rr("plc-a.local.", in, &dnsmessage.AAAAResource{AAAA: netip.MustParseAddr("fd00::5").As16()}),
		rr("5.0.0.10.in-addr.arpa.", in, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("plc-a.local.")}),
		rr("_http._tcp.local.", in, &dnsmessage.PTRResource{PTR: dnsmessage.MustNewName("web._http._tcp.local.")}),
		rr("web._http._tcp.local.", in, &dnsmessage.SRVResource{Target: dnsmessage.MustNewName("plc-a.local."), Port: 80}),
		rr("web._http._tcp.local.", in, &dnsmessage.TXTResource{TXT: []string{"path=/", ""}}),
		rr("www.plant.local.", in, &dnsmessage.CNAMEResource{CNAME: dnsmessage.MustNewName("plc-a.local.")}),
		// Not kept: a type the decoder does not read, an unusable address.
		rr("plant.local.", in, &dnsmessage.MXResource{MX: dnsmessage.MustNewName("mail.plant.local.")}),
		rr("plc-b.local.", in, &dnsmessage.AResource{A: [4]byte{0, 0, 0, 0}}),
	}
	soa := rr("plant.local.", in, &dnsmessage.SOAResource{NS: dnsmessage.MustNewName("ns.plant.local."),
		MBox: dnsmessage.MustNewName("admin.plant.local."), MinTTL: 60})
	ns := rr("plant.local.", in, &dnsmessage.NSResource{NS: dnsmessage.MustNewName("ns.plant.local.")})

	res, err := ParseResponse(response(t, full, []dnsmessage.Resource{ns, soa}), true)
	if err != nil || res.ID != 7 || res.NegativeTTL != 60 {
		t.Fatalf("response: %v %+v", err, res)
	}
	var got []string
	for _, r := range res.Records {
		got = append(got, record(r))
	}
	if want := "A:plc-a.local>10.0.0.5 flush|AAAA:plc-a.local>fd00::5|PTR:plc-a.local>10.0.0.5|PTR:_http._tcp.local>web._http._tcp.local|" +
		"SRV:web._http._tcp.local>plc-a.local:80|TXT:web._http._tcp.local>path=/|CNAME:www.plant.local>plc-a.local"; strings.Join(got, "|") != want {
		t.Errorf("records:\n%s\nwant\n%s", strings.Join(got, "|"), want)
	}
	// Unicast DNS has no cache-flush bit: the class is read as it is.
	if res, err := ParseResponse(response(t, full[:1], nil), false); err != nil || len(res.Records) != 1 || res.Records[0].Flush {
		t.Errorf("unicast: %v %+v", err, res)
	}

	// A record cut off in its data stops the parse with an error; the
	// records before it are kept.
	for i, r := range full[:7] {
		b := response(t, []dnsmessage.Resource{full[0], r}, nil)
		res, err := ParseResponse(b[:len(b)-1], true)
		if err == nil || len(res.Records) != 1 {
			t.Errorf("record %d cut off: %v %+v", i, err, res)
		}
	}
	// A cut-off SOA leaves the negative TTL unknown, not an error.
	b := response(t, nil, []dnsmessage.Resource{soa})
	if res, err := ParseResponse(b[:len(b)-2], false); err != nil || res.NegativeTTL != 0 {
		t.Errorf("cut-off SOA: %v %+v", err, res)
	}
	for name, b := range map[string][]byte{
		"empty":     nil,
		"a query":   func() []byte { m, _ := (&dnsmessage.Message{Header: dnsmessage.Header{ID: 1}}).Pack(); return m }(),
		"questions": response(t, nil, nil)[:14],
	} {
		if _, err := ParseResponse(b, false); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// Captured responses (mDNS and unicast DNS) that are cut off keep the
// records read before the cut; a message cut before its answers, or no
// message, teaches nothing.
func TestCapturedDNSCutOff(t *testing.T) {
	a := rr("plc-a.local.", dnsmessage.ClassINET, &dnsmessage.AResource{A: [4]byte{10, 0, 0, 5}})
	b := response(t, []dnsmessage.Resource{a, a}, nil)
	src := netip.MustParseAddr("10.0.0.5")
	for _, tt := range []struct {
		name string
		msg  []byte
		want int // observations, each with one record
	}{
		{"cut in the second record", b[:len(b)-1], 1},
		{"cut in the questions", b[:14], 0},
		{"empty", nil, 0},
	} {
		for proto, decode := range map[string]func(f *frame){
			"mdns": func(f *frame) { f.mdns(src, tt.msg) },
			"dns":  func(f *frame) { f.dns(tt.msg) },
		} {
			f := newFrame()
			decode(f)
			if len(f.out) != tt.want || tt.want > 0 && len(f.out[0].Records) != 1 {
				t.Errorf("%s %s: %v", proto, tt.name, summaries(f.out))
			}
		}
	}
}
