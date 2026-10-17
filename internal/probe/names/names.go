// Package names is the name query engine (FR-AC-12, docs/ARCHITECTURE.md
// §5): paced DNS and mDNS questions about names and addresses already
// known, and DNS-SD types the operator configured. Every query send is one
// start of the shared budget. Answers are name records only: they never
// say which MAC holds an address, and a failure or a negative answer
// changes nothing that is recorded.
package names

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/otfabric/lan-tracker/internal/collect/capture/decoders"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/probe"
)

// MDNSGroup is where one-shot mDNS queries go (RFC 6762 §5.1).
var MDNSGroup = netip.MustParseAddrPort("224.0.0.251:5353")

// Limits of one query.
const (
	maxResponders = 16   // mDNS answers read per query
	maxCNAMEs     = 2    // CNAME targets followed per DNS question
	maxMessage    = 9000 // bytes read per answer (an mDNS packet's limit)
	// negativeFloor and negativeCeiling bound how long a negative answer
	// keeps a question from being asked again (RFC 2308).
	negativeFloor   = 5 * time.Minute
	negativeCeiling = time.Hour
)

// Engine sends name queries through the platform transmitter. It keeps
// negative answers for their TTL, so a name that does not exist is not
// asked about every pass.
type Engine struct {
	TX platform.Transmitter

	mu       sync.Mutex
	negative map[string]time.Time // question → not before
}

// Protocol implements probe.Engine.
func (*Engine) Protocol() probe.Protocol { return probe.Name }

func question(q probe.NameQuery) string {
	return fmt.Sprintf("%s/%d/%s", strings.ToLower(q.Name), q.Type, q.Server)
}

// Run implements probe.Engine: each query of the pass in order, one start
// each (a CNAME followed is another). A question with a fresh negative
// answer is skipped without sending.
func (e *Engine) Run(ctx context.Context, p probe.Pass) ([]probe.Result, error) {
	var results []probe.Result
	for _, q := range p.Names {
		if e.negativeUntil(question(q)).After(p.Now()) {
			continue
		}
		name := q.Name
		for hop := 0; ; hop++ {
			release, err := p.Budget.Acquire(ctx, probe.Name, p.Link.Name, q.Server.Addr())
			if errors.Is(err, probe.ErrRefused) {
				results = append(results, probe.Result{Target: q.Server.Addr(), State: probe.Blocked})
				break
			}
			if err != nil {
				return results, err
			}
			ask := q
			ask.Name = name
			res, cname, err := e.ask(ctx, p, ask)
			release()
			if err != nil {
				return results, err
			}
			results = append(results, probe.Result{Target: q.Server.Addr(), State: res})
			if cname == "" || q.MDNS || hop >= maxCNAMEs {
				break
			}
			name = cname // a CNAME without the asked record: follow it, bounded
		}
	}
	return results, nil
}

func (e *Engine) negativeUntil(q string) time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.negative[q]
}

func (e *Engine) rememberNegative(q string, until time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.negative == nil {
		e.negative = map[string]time.Time{}
	}
	if len(e.negative) > 4096 {
		clear(e.negative) // bounded; forgetting only asks again
	}
	e.negative[q] = until
}

// ask sends one query and reads its answers until the reply timeout: every
// responder's for mDNS, the resolver's one for DNS. It reports reply or
// no_reply, and for DNS the CNAME target to follow when the answer holds
// only a CNAME.
func (e *Engine) ask(ctx context.Context, p probe.Pass, q probe.NameQuery) (string, string, error) {
	id := rand.N[uint16](math.MaxUint16)
	msg, err := build(id, q)
	if err != nil {
		return probe.NoReply, "", err
	}
	var conn interface {
		SetReadDeadline(time.Time) error
		Close() error
	}
	var read func([]byte) (int, netip.Addr, error)
	if q.MDNS {
		pc, err := e.TX.ListenUDP(ctx, p.Link.Name)
		if err != nil {
			return probe.NoReply, "", fmt.Errorf("names on %s: %w", p.Link.Name, err)
		}
		conn = pc
		if _, err := pc.WriteTo(msg, net.UDPAddrFromAddrPort(q.Server)); err != nil {
			_ = pc.Close()
			return probe.NoReply, "", fmt.Errorf("names on %s: %w", p.Link.Name, err)
		}
		read = func(b []byte) (int, netip.Addr, error) {
			n, from, err := pc.ReadFrom(b)
			var addr netip.Addr
			if u, ok := from.(*net.UDPAddr); ok {
				addr = u.AddrPort().Addr().Unmap()
			}
			return n, addr, err
		}
	} else {
		c, err := e.TX.DialUDP(ctx, p.Link.Name, q.Server)
		if err != nil {
			return probe.NoReply, "", fmt.Errorf("names on %s: %w", p.Link.Name, err)
		}
		conn = c
		if _, err := c.Write(msg); err != nil {
			_ = c.Close()
			return probe.NoReply, "", fmt.Errorf("names on %s: %w", p.Link.Name, err)
		}
		read = func(b []byte) (int, netip.Addr, error) {
			n, err := c.Read(b)
			return n, q.Server.Addr(), err
		}
	}
	defer func() { _ = conn.Close() }() // a UDP socket: nothing to flush
	_ = conn.SetReadDeadline(time.Now().Add(p.Timeout()))
	state, cname := probe.NoReply, ""
	buf := make([]byte, maxMessage)
	for range maxResponders {
		if ctx.Err() != nil {
			break
		}
		n, from, err := read(buf)
		if err != nil {
			break // the deadline, or the socket closed
		}
		res, err := decoders.ParseResponse(buf[:n], q.MDNS)
		if err != nil || res.ID != id && !q.MDNS {
			continue // not ours, or malformed: nothing learnt
		}
		state = probe.Reply
		if !q.MDNS {
			cname = e.dnsOutcome(p, q, res)
		}
		if len(res.Records) > 0 {
			o := observation.Observation{Time: p.Now(), Source: observation.DNSQuery, Interface: p.Link.Name, Records: res.Records}
			if q.MDNS {
				// The responder's address, not its MAC: the socket does not
				// see frames, and records bind nothing to the responder.
				o.Source, o.IP = observation.MDNSQuery, from
			}
			p.Emit(o)
		}
		if !q.MDNS {
			break
		}
	}
	return state, cname, nil
}

// dnsOutcome keeps a negative answer for its TTL and returns the CNAME to
// follow when the answer holds a CNAME but none of the asked records.
func (e *Engine) dnsOutcome(p probe.Pass, q probe.NameQuery, res decoders.Response) string {
	asked, cname := false, ""
	for _, r := range res.Records {
		switch {
		case r.Type == observation.RecordCNAME && strings.EqualFold(r.Name, strings.TrimSuffix(q.Name, ".")):
			cname = r.Value
		case recordType(r.Type) == q.Type:
			asked = true
		}
	}
	if asked {
		return ""
	}
	if cname == "" && (res.RCode == dnsmessage.RCodeNameError || res.RCode == dnsmessage.RCodeSuccess) {
		ttl := min(max(time.Duration(res.NegativeTTL)*time.Second, negativeFloor), negativeCeiling)
		e.rememberNegative(question(q), p.Now().Add(ttl))
	}
	return cname
}

func recordType(t observation.RecordType) uint16 {
	switch t {
	case observation.RecordA:
		return uint16(dnsmessage.TypeA)
	case observation.RecordAAAA:
		return uint16(dnsmessage.TypeAAAA)
	case observation.RecordPTR:
		return uint16(dnsmessage.TypePTR)
	}
	return 0
}

// build encodes one question. A unicast DNS query asks for recursion; an
// mDNS one-shot query is sent from an ephemeral port, so responders answer
// it by unicast (RFC 6762 §5.1).
func build(id uint16, q probe.NameQuery) ([]byte, error) {
	name := q.Name
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	n, err := dnsmessage.NewName(name)
	if err != nil {
		return nil, fmt.Errorf("query %q: %w", q.Name, err)
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: id, RecursionDesired: !q.MDNS})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(dnsmessage.Question{Name: n, Type: dnsmessage.Type(q.Type), Class: dnsmessage.ClassINET}); err != nil {
		return nil, err
	}
	return b.Finish()
}

// ReverseName is the PTR question for an address ("4.3.2.1.in-addr.arpa").
func ReverseName(ip netip.Addr) string {
	a := ip.Unmap().As4()
	return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", a[3], a[2], a[1], a[0])
}
