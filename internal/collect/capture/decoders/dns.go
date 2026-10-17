package decoders

import (
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/otfabric/lan-tracker/internal/observation"
)

// maxRecords bounds the resource records read from one DNS or mDNS
// message.
const maxRecords = 64

// cacheFlush is the top bit of an mDNS record's class (RFC 6762 §10.2).
const cacheFlush = 0x8000

// mdns decodes an mDNS response (FR-PA-4) into name records: A and AAAA
// (a name and the address it advertises), PTR (a service type and an
// instance), SRV (an instance's target and port) and TXT (bounded text),
// each with its TTL and cache-flush bit. The sender is the frame's source
// MAC and address. The records are what the sender advertises: a proxy,
// a reflector or a host answering for others advertises addresses that
// are not its own, so the records never bind an address to the sender
// (docs/DATA_MODEL.md §5.4). Records read before a malformed one are kept.
func (f *frame) mdns(src netip.Addr, b []byte) {
	var p dnsmessage.Parser
	h, err := p.Start(b)
	if err != nil || !h.Response || h.RCode != dnsmessage.RCodeSuccess {
		return
	}
	if err := p.SkipAllQuestions(); err != nil {
		return
	}
	var records []observation.Record
	n := 0
	visit := func(next func() (dnsmessage.ResourceHeader, error), skip func() error) bool {
		for n < maxRecords {
			rh, err := next()
			if err != nil {
				return errors.Is(err, dnsmessage.ErrSectionDone)
			}
			n++
			r, ok, err := readRecord(&p, rh, skip, true)
			if err != nil {
				return false
			}
			if ok {
				records = append(records, r)
			}
		}
		return true
	}
	if visit(p.AnswerHeader, p.SkipAnswer) && n < maxRecords && p.SkipAllAuthorities() == nil {
		visit(p.AdditionalHeader, p.SkipAdditional)
	}
	if len(records) == 0 {
		return
	}
	f.add(observation.Observation{Source: observation.PassiveMDNS, MAC: f.src, IP: src, Records: records})
}

// readRecord reads one resource record. ok is false for a type it does
// not keep (skipped with skip) or one without a usable name; err stops
// parsing. With mdns, the class's top bit is the cache-flush bit.
func readRecord(p *dnsmessage.Parser, rh dnsmessage.ResourceHeader, skip func() error, mdns bool) (observation.Record, bool, error) {
	r := observation.Record{Name: hostname(rh.Name.String()), TTL: observation.TTLOf(rh.TTL)}
	if mdns {
		r.Flush = uint16(rh.Class)&cacheFlush != 0
	}
	switch rh.Type {
	case dnsmessage.TypeA:
		a, err := p.AResource()
		if err != nil {
			return r, false, err
		}
		r.Type, r.Addr = observation.RecordA, netip.AddrFrom4(a.A)
	case dnsmessage.TypeAAAA:
		a, err := p.AAAAResource()
		if err != nil {
			return r, false, err
		}
		r.Type, r.Addr = observation.RecordAAAA, netip.AddrFrom16(a.AAAA)
	case dnsmessage.TypePTR:
		ptr, err := p.PTRResource()
		if err != nil {
			return r, false, err
		}
		target := hostname(ptr.PTR.String())
		if ip, ok := reverseAddr(rh.Name.String()); ok {
			// A reverse lookup: the name an address has.
			r.Type, r.Name, r.Addr = observation.RecordPTR, target, ip
		} else {
			// Service enumeration: a type and one of its instances.
			r.Type, r.Value = observation.RecordPTR, target
		}
	case dnsmessage.TypeSRV:
		srv, err := p.SRVResource()
		if err != nil {
			return r, false, err
		}
		r.Type, r.Value = observation.RecordSRV, hostname(srv.Target.String())+":"+strconv.Itoa(int(srv.Port))
	case dnsmessage.TypeTXT:
		txt, err := p.TXTResource()
		if err != nil {
			return r, false, err
		}
		var parts []string
		for _, s := range txt.TXT {
			if t := text([]byte(s), maxText); t != "" {
				parts = append(parts, t)
			}
		}
		r.Type, r.Value = observation.RecordTXT, text([]byte(strings.Join(parts, ";")), maxText)
	case dnsmessage.TypeCNAME:
		c, err := p.CNAMEResource()
		if err != nil {
			return r, false, err
		}
		r.Type, r.Value = observation.RecordCNAME, hostname(c.CNAME.String())
	default:
		return r, false, skip()
	}
	if r.Name == "" || r.Type == observation.RecordPTR && !r.Addr.IsValid() && r.Value == "" {
		return r, false, nil
	}
	if r.Addr.IsValid() && !usableIP(r.Addr) {
		return r, false, nil
	}
	return r, true, nil
}

// dns decodes a unicast DNS response's answers: reverse PTR, A, AAAA and
// CNAME records (name types dns_ptr and dns). They describe names and
// addresses, never which MAC holds an address: the frame comes from a
// resolver, so the observation carries no MAC (docs/DATA_MODEL.md §5.4).
// Negative answers and failures say nothing and are skipped.
func (f *frame) dns(b []byte) {
	var p dnsmessage.Parser
	h, err := p.Start(b)
	if err != nil || !h.Response || h.RCode != dnsmessage.RCodeSuccess {
		return
	}
	if err := p.SkipAllQuestions(); err != nil {
		return
	}
	var records []observation.Record
answers:
	for range maxRecords {
		rh, err := p.AnswerHeader()
		if err != nil {
			break
		}
		switch rh.Type {
		case dnsmessage.TypePTR, dnsmessage.TypeA, dnsmessage.TypeAAAA, dnsmessage.TypeCNAME:
		default:
			if p.SkipAnswer() != nil {
				break answers
			}
			continue
		}
		r, ok, err := readRecord(&p, rh, p.SkipAnswer, false)
		if err != nil {
			break
		}
		if ok && (r.Type != observation.RecordPTR || r.Addr.IsValid()) && !slices.ContainsFunc(records, func(x observation.Record) bool {
			return x.Type == r.Type && x.Name == r.Name && x.Addr == r.Addr && x.Value == r.Value
		}) {
			records = append(records, r)
		}
	}
	if len(records) > 0 {
		f.add(observation.Observation{Source: observation.PassiveDNS, Records: records})
	}
}

// reverseAddr parses "4.3.2.1.in-addr.arpa." and the nibble form under
// ip6.arpa.
func reverseAddr(name string) (netip.Addr, bool) {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if rest, ok := strings.CutSuffix(name, ".in-addr.arpa"); ok {
		parts := strings.Split(rest, ".")
		if len(parts) != 4 {
			return netip.Addr{}, false
		}
		slices.Reverse(parts)
		ip, err := netip.ParseAddr(strings.Join(parts, "."))
		return ip, err == nil
	}
	if rest, ok := strings.CutSuffix(name, ".ip6.arpa"); ok {
		nibbles := strings.Split(rest, ".")
		if len(nibbles) != 32 {
			return netip.Addr{}, false
		}
		var b [16]byte
		for i, n := range nibbles {
			v, err := strconv.ParseUint(n, 16, 8)
			if err != nil || len(n) != 1 {
				return netip.Addr{}, false
			}
			pos := 31 - i // nibbles are listed least significant first
			b[pos/2] |= byte(v) << (4 * (1 - pos%2))
		}
		return netip.AddrFrom16(b), true
	}
	return netip.Addr{}, false
}

// Response is a parsed DNS or mDNS response to one of LAN Tracker's own
// queries (FR-AC-12).
type Response struct {
	ID      uint16
	RCode   dnsmessage.RCode
	Records []observation.Record
	// NegativeTTL is the SOA minimum of a negative answer (RFC 2308), 0
	// without one.
	NegativeTTL uint32
}

// ParseResponse reads a response's answer and additional records (at most
// maxRecords, bounded like captured ones) and, for a negative answer, the
// SOA minimum of its authority section.
func ParseResponse(b []byte, mdns bool) (Response, error) {
	var p dnsmessage.Parser
	h, err := p.Start(b)
	if err != nil {
		return Response{}, err
	}
	if !h.Response {
		return Response{}, errors.New("not a response")
	}
	res := Response{ID: h.ID, RCode: h.RCode}
	if err := p.SkipAllQuestions(); err != nil {
		return res, err
	}
	n := 0
	read := func(next func() (dnsmessage.ResourceHeader, error), skip func() error) error {
		for n < maxRecords {
			rh, err := next()
			if errors.Is(err, dnsmessage.ErrSectionDone) {
				return nil
			}
			if err != nil {
				return err
			}
			n++
			r, ok, err := readRecord(&p, rh, skip, mdns)
			if err != nil {
				return err
			}
			if ok {
				res.Records = append(res.Records, r)
			}
		}
		return nil
	}
	if err := read(p.AnswerHeader, p.SkipAnswer); err != nil {
		return res, err
	}
	for {
		rh, err := p.AuthorityHeader()
		if err != nil {
			break
		}
		if rh.Type != dnsmessage.TypeSOA {
			if p.SkipAuthority() != nil {
				break
			}
			continue
		}
		soa, err := p.SOAResource()
		if err != nil {
			break
		}
		res.NegativeTTL = min(soa.MinTTL, rh.TTL)
	}
	if err := p.SkipAllAuthorities(); err == nil {
		_ = read(p.AdditionalHeader, p.SkipAdditional) // additional records are a bonus
	}
	return res, nil
}
