package probe

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/store"
)

// DefaultTCPTimeout is the connect timeout of a port that is not among
// active.tcp.targets (an operator scan of another port).
const DefaultTCPTimeout = config.DefaultTCPTimeout

// Request is an operator scan (`scan plan`, `scan run`, docs/CLI.md).
// Probes named here are added to the profile's; with none at all the scan
// is an ARP sweep.
type Request struct {
	Interfaces []string       `json:"interfaces,omitempty"` // empty: every interface with active discovery
	Networks   []netip.Prefix `json:"networks,omitempty"`   // empty: the interfaces' configured networks
	Profile    string         `json:"profile,omitempty"`
	ARP        bool           `json:"arp,omitempty"`
	ICMP       bool           `json:"icmp,omitempty"`
	TCP        []int          `json:"tcp,omitempty"`
	// AllowWide acknowledges a sweep of more than
	// config.DefaultMaxSweepTargets addresses, after seeing its plan.
	AllowWide bool `json:"allow_wide,omitempty"`
}

// Probes are the probes of a scan. ARP sweeps the networks; ICMP and TCP
// probe the known hosts in them (and, in a run, the ARP responders).
type Probes struct {
	ARP  bool  `json:"arp"`
	ICMP bool  `json:"icmp"`
	TCP  []int `json:"tcp,omitempty"`
}

// Plan is the dry run of a scan: what it would probe, at what rates, for
// how long, and whether it is allowed. `scan run` refuses exactly when the
// plan does.
type Plan struct {
	Allowed    bool            `json:"allowed"`
	Reasons    []string        `json:"reasons,omitempty"`
	Profile    string          `json:"profile,omitempty"`
	Probes     Probes          `json:"probes"`
	Interfaces []InterfacePlan `json:"interfaces"`
	Rates      Rates           `json:"rates"`
	Estimate   Estimate        `json:"estimate"`
	// Assumptions are what the estimate rests on.
	Assumptions []string `json:"assumptions"`
}

// InterfacePlan is the part of a scan on one interface.
type InterfacePlan struct {
	Interface    string         `json:"interface"`
	Networks     []netip.Prefix `json:"networks"`
	SweepTargets int            `json:"sweep_targets"` // ARP: addresses in the networks after excludes
	KnownTargets int            `json:"known_targets"` // ICMP/TCP: known hosts in the networks
	Excluded     []string       `json:"excluded,omitempty"`
	// NoDirectRoute lists the known hosts a TCP check cannot reach on
	// this interface, with why (routed via a gateway, no source address):
	// they are not dialled, and the run records them as LOCAL_ERROR.
	NoDirectRoute []string `json:"no_direct_route,omitempty"`
	Reasons       []string `json:"reasons,omitempty"`
	Estimate      Estimate `json:"estimate"`
	tcpKnown      []netip.Addr

	Sweep Sweep                 `json:"-"`
	Known []netip.Addr          `json:"-"`
	TCP   []config.TCPTarget    `json:"-"`
	Link  Link                  `json:"-"`
	Own   []netip.Addr          `json:"-"`
	Excl  []config.AddrOrPrefix `json:"-"`
}

// Rates are the limits a scan runs under.
type Rates struct {
	GlobalStarts    float64              `json:"global_starts_per_second"`
	Protocols       map[Protocol]float64 `json:"protocols"`
	MaxConcurrent   int                  `json:"max_concurrent"`
	TCPPerInterface int                  `json:"tcp_per_interface"`
	TCPPerHost      int                  `json:"tcp_per_host"`
	TargetSpacingMS int64                `json:"target_spacing_ms"`
}

// Estimate is the expected work of a scan: probes per protocol, probe
// starts (every probe is one; the kernel's TCP packets are not counted)
// and two durations: the estimated typical
// duration, where known hosts answer at once and only the ARP sweep waits
// out its reply timeout (most swept addresses are empty), and the
// estimated no-response duration, where nothing answers and every phase
// waits out its reply timeout. Plan.Assumptions spell out the rest.
type Estimate struct {
	ARPRequests       int     `json:"arp_requests"`
	ICMPEchoes        int     `json:"icmp_echoes"`
	TCPConnects       int     `json:"tcp_connects"`
	Starts            int     `json:"starts"`
	TypicalSeconds    float64 `json:"typical_seconds"`
	NoResponseSeconds float64 `json:"no_response_seconds"`
}

func (e *Estimate) add(o Estimate) {
	e.ARPRequests += o.ARPRequests
	e.ICMPEchoes += o.ICMPEchoes
	e.TCPConnects += o.TCPConnects
	e.Starts += o.Starts
	e.TypicalSeconds += o.TypicalSeconds
	e.NoResponseSeconds += o.NoResponseSeconds
}

// PlanInput is what a plan is computed from: online, the daemon's
// effective configuration, kill switch, live links and known hosts;
// offline, the configuration file, the persisted switch and the database.
type PlanInput struct {
	Config *config.Config
	Active store.ActiveState
	Links  map[string]Link         // live interface addresses (own IPs are never probed)
	Known  map[string][]netip.Addr // known IPv4 addresses per interface
	// Route, when set, is the kernel's route lookup (online plans).
	Route func(iface string, ip netip.Addr) (platform.Route, error)
}

// Compute plans req. Every refusal reason is listed, not only the first.
func Compute(in PlanInput, req Request) Plan {
	cfg := in.Config
	a := cfg.Active
	limits := LimitsFrom(a)
	p := Plan{Profile: req.Profile, Rates: Rates{
		GlobalStarts: limits.GlobalStarts, Protocols: limits.Rates, MaxConcurrent: limits.MaxConcurrent,
		TCPPerInterface: limits.TCPPerInterface, TCPPerHost: limits.TCPPerHost,
		TargetSpacingMS: limits.TargetSpacing.Milliseconds(),
	}}
	refuse := func(format string, args ...any) { p.Reasons = append(p.Reasons, fmt.Sprintf(format, args...)) }

	if in.Active.Disabled {
		refuse("active discovery is disabled by the kill switch%s", why(in.Active.Reason))
	}
	p.Probes = probesOf(cfg, req, refuse)

	names := req.Interfaces
	if len(names) == 0 {
		for _, ic := range cfg.Interfaces {
			if ic.Active.Enabled {
				names = append(names, ic.Name)
			}
		}
		if len(names) == 0 {
			refuse("no interface has active discovery enabled")
		}
	}
	for _, n := range req.Networks {
		if !n.Addr().Is4() {
			refuse("%s: v1 scans IPv4 networks only", n)
		} else if n.Bits() < a.MaxAutoScanPrefixV4 && !a.AllowWideScan {
			refuse("%s is wider than max_auto_scan_prefix_v4 /%d", n, a.MaxAutoScanPrefixV4)
		}
	}
	used := map[netip.Prefix]bool{}
	for _, name := range names {
		ip := planInterface(cfg, in, name, req, p.Probes, used)
		p.Interfaces = append(p.Interfaces, ip)
		p.Estimate.add(ip.Estimate)
	}
	for _, n := range req.Networks {
		if !used[n.Masked()] && n.Addr().Is4() {
			refuse("%s is outside the configured networks of %s", n, joinNames(names))
		}
	}
	for _, ip := range p.Interfaces {
		for _, r := range ip.Reasons {
			refuse("%s: %s", ip.Interface, r)
		}
	}
	p.Assumptions = assumptions(p)
	p.Allowed = len(p.Reasons) == 0
	return p
}

// assumptions explain the estimate of p.
func assumptions(p Plan) []string {
	pr := p.Probes
	known := pr.ICMP || len(pr.TCP) > 0
	spacing := time.Duration(p.Rates.TargetSpacingMS) * time.Millisecond
	out := []string{fmt.Sprintf("probes start paced at the rates above, at most %g starts/s in all, at least %v apart per target "+
		"and at most %d in flight, each holding its slot while it waits for a reply; sending, connecting and answering take no time; "+
		"the kernel's TCP handshake, retransmission and close packets are not counted", p.Rates.GlobalStarts, spacing, p.Rates.MaxConcurrent)}
	switch {
	case pr.ARP && known:
		out = append(out, "typical: known hosts answer at once; most swept addresses are empty, so the ARP sweep waits its 1s reply timeout once, after its last request")
	case pr.ARP:
		out = append(out, "typical: most swept addresses are empty, so the sweep waits its 1s reply timeout once, after its last request")
	default:
		out = append(out, "typical: known hosts answer at once")
	}
	var waits []string
	if pr.ARP {
		waits = append(waits, "ARP "+DefaultReplyTimeout.String())
	}
	if pr.ICMP {
		waits = append(waits, "ICMP "+DefaultReplyTimeout.String())
	}
	if len(p.Interfaces) > 0 {
		for _, t := range p.Interfaces[0].TCP {
			waits = append(waits, fmt.Sprintf("tcp/%d %v", t.Port, t.Timeout.D()))
		}
	}
	out = append(out, "no-response: nothing answers, so every phase waits out its reply timeout once ("+strings.Join(waits, ", ")+")")
	if pr.ARP && known {
		out = append(out, "ICMP and TCP counts use the hosts known now; hosts that first answer the sweep are probed too, which adds to a run")
	}
	return out
}

func why(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

func joinNames(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return fmt.Sprint(names)
}

// probesOf merges the profile with the request's own probes.
func probesOf(cfg *config.Config, req Request, refuse func(string, ...any)) Probes {
	pr := Probes{ARP: req.ARP, ICMP: req.ICMP, TCP: slices.Clone(req.TCP)}
	if req.Profile != "" {
		prof, ok := cfg.Profiles[req.Profile]
		if !ok {
			refuse("unknown profile %q", req.Profile)
		}
		pr.ARP, pr.ICMP = pr.ARP || prof.ARP, pr.ICMP || prof.ICMP
		pr.TCP = append(pr.TCP, prof.TCP...)
	}
	if !pr.ARP && !pr.ICMP && len(pr.TCP) == 0 {
		pr.ARP = true
	}
	slices.Sort(pr.TCP)
	pr.TCP = slices.Compact(pr.TCP)
	for _, port := range pr.TCP {
		if port < 1 || port > 65535 {
			refuse("TCP port %d is out of range", port)
		}
	}
	return pr
}

func planInterface(cfg *config.Config, in PlanInput, name string, req Request, pr Probes, used map[netip.Prefix]bool) InterfacePlan {
	ip := InterfacePlan{Interface: name, Link: in.Links[name]}
	ip.Link.Name = name
	ic, ok := InterfaceConfig(cfg, name)
	switch {
	case !ok:
		ip.Reasons = append(ip.Reasons, "interface is not configured")
		return ip
	case !ic.Active.Enabled:
		ip.Reasons = append(ip.Reasons, "active discovery is disabled on the interface")
		return ip
	}
	ip.Networks = ic.Active.Networks
	if len(req.Networks) > 0 {
		ip.Networks = nil
		for _, n := range req.Networks {
			if n.Addr().Is4() && within(ic.Active.Networks, n) {
				ip.Networks = append(ip.Networks, n.Masked())
				used[n.Masked()] = true
			}
		}
		if len(ip.Networks) == 0 {
			ip.Reasons = append(ip.Reasons, "none of the requested networks is configured on the interface")
			return ip
		}
	}
	ip.Own, ip.Excl = ip.Link.Own(), ic.Active.Exclude
	if pr.ARP {
		ip.Sweep = NewSweep(ip.Networks, ip.Excl, ip.Own)
		ip.SweepTargets, ip.Excluded = ip.Sweep.Len(), ip.Sweep.Excluded()
		switch limit := cfg.Active.MaxSweepTargets; {
		case ip.SweepTargets > limit:
			ip.Reasons = append(ip.Reasons, fmt.Sprintf("the sweep covers %d addresses, more than active.max_sweep_targets (%d)", ip.SweepTargets, limit))
		case ip.SweepTargets > config.DefaultMaxSweepTargets && !req.AllowWide:
			ip.Reasons = append(ip.Reasons, fmt.Sprintf("the sweep covers %d addresses, more than %d: review this plan, then repeat with --allow-wide",
				ip.SweepTargets, config.DefaultMaxSweepTargets))
		}
	}
	if pr.ICMP || len(pr.TCP) > 0 {
		ip.Known = KnownTargets(in.Known[name], ip.Networks, ip.Excl, ip.Own)
		ip.KnownTargets = len(ip.Known)
	}
	ip.TCP = tcpTargets(cfg, pr.TCP)
	ip.tcpKnown = ip.Known
	if len(ip.TCP) > 0 && in.Route != nil {
		ip.tcpKnown = nil
		for _, k := range ip.Known {
			rt, err := in.Route(name, k)
			switch {
			case err != nil:
				ip.NoDirectRoute = append(ip.NoDirectRoute, k.String()+": no route via "+name)
			case !rt.Direct(name):
				ip.NoDirectRoute = append(ip.NoDirectRoute, k.String()+": "+rt.Problem(name))
			default:
				ip.tcpKnown = append(ip.tcpKnown, k)
			}
		}
	}
	if ip.SweepTargets == 0 && ip.KnownTargets == 0 {
		ip.Reasons = append(ip.Reasons, "no targets left")
	}
	ip.Estimate = estimate(LimitsFrom(cfg.Active), ip, pr)
	return ip
}

// within reports whether n lies inside one of the networks.
func within(networks []netip.Prefix, n netip.Prefix) bool {
	for _, c := range networks {
		if c.Bits() <= n.Bits() && c.Contains(n.Addr()) {
			return true
		}
	}
	return false
}

// tcpTargets uses the configured name and timeout of a port, if any.
func tcpTargets(cfg *config.Config, ports []int) []config.TCPTarget {
	out := make([]config.TCPTarget, 0, len(ports))
	for _, port := range ports {
		t := config.TCPTarget{Port: port, Timeout: config.Duration(DefaultTCPTimeout)}
		for _, c := range cfg.Active.TCP.Targets {
			if c.Port == port {
				t = c
			}
		}
		out = append(out, t)
	}
	return out
}

// simSweep is how much of a long sweep the estimate simulates; the rest
// goes at the steady pace the simulation reached.
const simSweep = 20000

// estimate counts the probes of one interface and simulates them in the
// order and phases a run sends them: the ARP sweep (known hosts first),
// then ICMP, then TCP port by port, the known hosts in every phase. Every
// probe holds a concurrency slot for its reply wait, so with few slots an
// unanswered sweep runs below its start rate. Typical: the known hosts
// answer at once, the other swept addresses wait out the 1 s reply
// timeout; no-response: every probe waits out its timeout.
func estimate(limits Limits, ip InterfacePlan, pr Probes) Estimate {
	var e Estimate
	type phaseOf struct {
		p             Protocol
		targets       []netip.Addr
		typical, wait time.Duration
	}
	var sweep []netip.Addr
	known := map[netip.Addr]bool{}
	for _, t := range ip.Known {
		known[t] = true
	}
	if pr.ARP && ip.SweepTargets > 0 {
		e.ARPRequests = ip.SweepTargets
		for _, t := range ip.Known {
			if ip.Sweep.Contains(t) {
				sweep = append(sweep, t)
			}
		}
		for _, t := range ip.Sweep.First(simSweep) {
			if !known[t] && len(sweep) < simSweep {
				sweep = append(sweep, t)
			}
		}
	}
	var phases []phaseOf
	if pr.ICMP {
		e.ICMPEchoes = len(ip.Known)
		phases = append(phases, phaseOf{ICMP, ip.Known, 0, DefaultReplyTimeout})
	}
	for _, t := range ip.TCP {
		e.TCPConnects += len(ip.tcpKnown)
		phases = append(phases, phaseOf{TCP, ip.tcpKnown, 0, t.Timeout.D()})
	}
	e.Starts = e.ARPRequests + e.ICMPEchoes + e.TCPConnects
	if e.Starts == 0 {
		return e
	}
	simulate := func(worst bool) time.Duration {
		hold := func(t netip.Addr, typical, wait time.Duration) time.Duration {
			if worst {
				return wait
			}
			return typical
		}
		var arp []SimProbe
		for _, t := range sweep {
			h := DefaultReplyTimeout
			if known[t] && !worst {
				h = 0
			}
			arp = append(arp, SimProbe{Protocol: ARP, Interface: ip.Interface, Target: t, Hold: h})
		}
		var rest [][]SimProbe
		for _, ph := range phases {
			var probes []SimProbe
			for _, t := range ph.targets {
				probes = append(probes, SimProbe{Protocol: ph.p, Interface: ip.Interface, Target: t, Hold: hold(t, ph.typical, ph.wait)})
			}
			rest = append(rest, probes)
		}
		if e.ARPRequests <= len(arp) {
			return Simulate(limits, time.Unix(0, 0), append([][]SimProbe{arp}, rest...)...)
		}
		// A long sweep: simulate its start, extrapolate its steady pace,
		// then the known-host phases on their own.
		half := len(arp) / 2
		t := Simulate(limits, time.Unix(0, 0), arp)
		step := float64(t-Simulate(limits, time.Unix(0, 0), arp[:half])) / float64(len(arp)-half)
		d := t + time.Duration(step*float64(e.ARPRequests-len(arp)))
		if len(rest) > 0 {
			d += Simulate(limits, time.Unix(0, 0), rest...)
		}
		return d
	}
	e.TypicalSeconds, e.NoResponseSeconds = simulate(false).Seconds(), simulate(true).Seconds()
	return e
}
