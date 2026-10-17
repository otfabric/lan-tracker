//go:build nettest

package nettest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/afpacket"
	"github.com/gopacket/gopacket/layers"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"

	"github.com/otfabric/lan-tracker/internal/api"
	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/daemon"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/probe"
	"github.com/otfabric/lan-tracker/internal/probe/icmp"
	"github.com/otfabric/lan-tracker/internal/probe/scheduler"
	"github.com/otfabric/lan-tracker/internal/store"
)

// frame is one frame the runner sent, as the counter saw it.
type frame struct {
	at    time.Time
	kind  string // arp, arp-reply, arp-announce, arp-unicast, icmp, tcp-syn, tcp, tcp-data, udp, ipv4
	dst   netip.Addr
	port  uint16 // TCP and UDP: the source port, which tells connections apart
	dport uint16 // TCP and UDP: the destination port
}

// payload reports traffic LAN Tracker never sends (ADR 0011): TCP segments
// with data (a banner read, a TLS handshake, an HTTP or Modbus request)
// and UDP (NTP, EtherNet/IP, SNMP).
func (f frame) payload() bool { return f.kind == "tcp-data" || f.kind == "udp" }

// probe reports whether the frame is probe traffic: what the engines send
// and the ARP the kernel sends to resolve probed hosts, not the kernel's
// ARP replies to other hosts, its unicast re-checks of cached neighbours,
// or Docker's gratuitous ARP for the runner.
func (f frame) probe() bool {
	return f.kind != "arp-reply" && f.kind != "arp-announce" && f.kind != "arp-unicast"
}

// counter records every IPv4 and ARP frame the runner sends on an
// interface, with kernel timestamps, from an AF_PACKET ring without a
// filter (it sees outgoing frames, which the daemon's capture drops).
type counter struct {
	mu     sync.Mutex
	frames []frame
	stop   func()
}

func startCounter(t *testing.T, iface string, own net.HardwareAddr) *counter {
	t.Helper()
	tp, err := afpacket.NewTPacket(afpacket.OptInterface(iface), afpacket.TPacketVersion3,
		afpacket.OptBlockTimeout(10*time.Millisecond), afpacket.OptPollTimeout(100*time.Millisecond))
	if err != nil {
		t.Fatalf("counter: %v", err)
	}
	// Only the frames the runner sends, which the kernel marks
	// PACKET_OUTGOING. The Docker bridge reflects broadcasts back to the
	// port they came from (hairpin mode); such a copy arrives as an incoming
	// frame with the runner's source MAC, sometimes tens of milliseconds
	// late, so telling copies apart by time was not enough (CI once counted
	// one ARP request twice, 21 ms apart).
	prog, err := bpf.Assemble([]bpf.Instruction{
		bpf.LoadExtension{Num: bpf.ExtType},
		bpf.JumpIf{Cond: bpf.JumpEqual, Val: unix.PACKET_OUTGOING, SkipFalse: 1},
		bpf.RetConstant{Val: 1 << 16},
		bpf.RetConstant{Val: 0},
	})
	if err != nil {
		t.Fatalf("counter filter: %v", err)
	}
	if err := tp.SetBPF(prog); err != nil {
		tp.Close()
		t.Fatalf("counter filter: %v", err)
	}
	// The ring is mapped before the filter is attached: skip what came in
	// between.
	filtered := time.Now()
	c := &counter{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			data, ci, err := tp.ReadPacketData()
			if err != nil || ci.Timestamp.Before(filtered) {
				continue
			}
			if f, ok := classify(data, own); ok {
				f.at = ci.Timestamp
				c.mu.Lock()
				c.frames = append(c.frames, f)
				c.mu.Unlock()
			}
		}
	}()
	c.stop = func() { cancel(); <-done; tp.Close() }
	t.Cleanup(c.stop)
	return c
}

func (c *counter) all() []frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := slices.Clone(c.frames)
	slices.SortFunc(out, func(a, b frame) int { return a.at.Compare(b.at) })
	return out
}

func classify(data []byte, own net.HardwareAddr) (frame, bool) {
	var (
		eth  layers.Ethernet
		arp  layers.ARP
		ip4  layers.IPv4
		icmp layers.ICMPv4
		tcp  layers.TCP
		udp  layers.UDP
	)
	p := gopacket.NewDecodingLayerParser(layers.LayerTypeEthernet, &eth, &arp, &ip4, &icmp, &tcp, &udp)
	p.IgnoreUnsupported = true
	var decoded []gopacket.LayerType
	_ = p.DecodeLayers(data, &decoded)
	if len(decoded) == 0 || !bytes.Equal(eth.SrcMAC, own) {
		return frame{}, false
	}
	var f frame
	for _, l := range decoded {
		switch l {
		case layers.LayerTypeARP:
			f.kind, f.dst = "arp", netip.AddrFrom4([4]byte(arp.DstProtAddress))
			switch {
			case arp.Operation != layers.ARPRequest:
				f.kind = "arp-reply"
			case bytes.Equal(arp.SourceProtAddress, arp.DstProtAddress):
				f.kind = "arp-announce" // gratuitous: the runner's own address
			case !bytes.Equal(eth.DstMAC, layers.EthernetBroadcast):
				// The kernel re-checking a neighbour it has cached sends
				// unicast requests to the known MAC, seconds after traffic
				// to it (here capcheck's, before this test). The sweep only
				// broadcasts, and the kernel's ARP is not budgeted
				// (ARCHITECTURE.md §5); counting it once put an 11th
				// "request" in a second of a 10 pps sweep.
				f.kind = "arp-unicast"
			}
		case layers.LayerTypeIPv4:
			f.kind, f.dst = "ipv4", netip.AddrFrom4([4]byte(ip4.DstIP.To4()))
		case layers.LayerTypeICMPv4:
			if icmp.TypeCode.Type() == layers.ICMPv4TypeEchoRequest {
				f.kind = "icmp"
			}
		case layers.LayerTypeTCP:
			f.kind, f.port, f.dport = "tcp", uint16(tcp.SrcPort), uint16(tcp.DstPort)
			switch {
			case tcp.SYN && !tcp.ACK:
				f.kind = "tcp-syn"
			case len(tcp.Payload) > 0:
				f.kind = "tcp-data"
			}
		case layers.LayerTypeUDP:
			f.kind, f.port, f.dport = "udp", uint16(udp.SrcPort), uint16(udp.DstPort)
		}
	}
	return f, f.kind != ""
}

// starts marks the frames that are probe starts (ADR 0011): the engine's
// ARP requests, ICMP echoes and the first SYN of each TCP connection. The
// ARP the kernel sends to resolve a probed host (a request for the target
// of an IP packet that follows within 100 ms) and SYN retransmissions are
// kernel packets, as are the handshake's ACK and the RST that closes it.
func starts(frames []frame) map[int]bool {
	out := map[int]bool{}
	syns := map[[2]uint16]bool{}
	for i, f := range frames {
		switch f.kind {
		case "icmp":
			out[i] = true
		case "tcp-syn":
			a := f.dst.As4()
			k := [2]uint16{uint16(a[2])<<8 | uint16(a[3]), f.port}
			out[i], syns[k] = !syns[k], true
		case "arp":
			resolve := false
			for _, g := range frames[i+1:] {
				if g.at.After(f.at.Add(100 * time.Millisecond)) {
					break
				}
				resolve = resolve || g.dst == f.dst && (g.kind == "icmp" || g.kind == "tcp-syn")
			}
			out[i] = !resolve
		}
	}
	return out
}

// maxPerSecond is the most frames kept by keep in any one-second window,
// and where that window starts.
func maxPerSecond(frames []frame, keep func(frame) bool) (int, time.Time) {
	var kept []frame
	for _, f := range frames {
		if keep(f) {
			kept = append(kept, f)
		}
	}
	most, at := 0, time.Time{}
	for i, f := range kept {
		n := 0
		for _, g := range kept[i:] {
			if !g.at.Before(f.at.Add(time.Second)) {
				break
			}
			n++
		}
		if n > most {
			most, at = n, f.at
		}
	}
	return most, at
}

type liveDaemon struct {
	d       *daemon.Daemon
	log     *syncBuffer
	signals chan os.Signal
	errc    chan error
	client  *api.Client
}

func startDaemon(t *testing.T, cfgPath, socket string) *liveDaemon {
	t.Helper()
	l := &liveDaemon{log: &syncBuffer{}, signals: make(chan os.Signal, 1), errc: make(chan error, 1)}
	d, err := daemon.New(daemon.Options{Load: config.LoadOptions{Path: cfgPath}, Stderr: l.log, Signals: l.signals})
	if err != nil {
		t.Fatal(err)
	}
	l.d = d
	go func() { l.errc <- d.Run(context.Background()) }()
	select {
	case <-d.Ready():
	case err := <-l.errc:
		t.Fatalf("daemon: %v\n%s", err, l.log)
	case <-time.After(30 * time.Second):
		t.Fatalf("daemon not ready\n%s", l.log)
	}
	l.client = api.NewClient(socket, 30*time.Second)
	return l
}

func (l *liveDaemon) waitCollector(t *testing.T, iface, collector string, state platform.State) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, c := range l.d.Collectors() {
			if c.Interface == iface && c.Collector == collector && c.State == state {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s/%s never %s: %+v\n%s", iface, collector, state, l.d.Collectors(), l.log)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (l *liveDaemon) stop(t *testing.T) {
	t.Helper()
	l.signals <- syscall.SIGTERM
	if err := <-l.errc; err != nil {
		t.Fatalf("daemon: %v\n%s", err, l.log)
	}
}

// TestActiveDiscovery is the active-discovery exit criterion in Docker, as
// uid 65534 with only CAP_NET_RAW: operator scans through the daemon find
// and probe the simulated hosts while every one-second window stays within
// the start budgets (the kernel's packets are measured and logged apart),
// excluded addresses are never probed, two probes to one target are at
// least the 5 s spacing apart, TCP results are classified, scan plan
// estimates the starts and the duration within 10%, the prefix guard
// refuses, no payload or UDP leaves the box, and the kill switch stops
// probing at once and survives a restart.
func TestActiveDiscovery(t *testing.T) {
	iface, prefix := env(t, "LT_TEST_IFACE"), netip.MustParsePrefix(env(t, "LT_TEST_PREFIX"))
	open, closed, plc, vanish := ip(t, "LT_TEST_OPEN"), ip(t, "LT_TEST_CLOSED"), ip(t, "LT_TEST_PLC"), ip(t, "LT_TEST_VANISH")
	swap, gone := ip(t, "LT_TEST_SWAP"), ip(t, "LT_TEST_GONE")
	own := ownMAC(t, iface)
	at := func(n byte) netip.Addr { a := prefix.Addr().As4(); a[3] = n; return netip.AddrFrom4(a) }
	lower := netip.PrefixFrom(prefix.Addr(), 25) // .0/25: the measured scan; vanish (.130) lies outside
	excluded := netip.PrefixFrom(at(200), 29)

	dir := t.TempDir()
	cfgPath, socket := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "api.sock")
	cfg := fmt.Sprintf(`version: 1
interfaces:
  - name: %s
    passive: { enabled: false }
    active:
      enabled: true
      networks: [%s]
      exclude: [%s, %s, %s]
active:
  startup_delay: 24h   # operator scans only
  tcp:
    enabled: false
    targets:
      - { port: 502, name: measured, timeout: 750ms }
      - { port: 503, name: unreachable, timeout: 4s }
profiles:
  all: { arp: true, icmp: true, tcp: [502] }
storage: { path: %s }
api: { socket: %s }
logging: { format: text }
`, iface, prefix, at(1), plc, excluded, filepath.Join(dir, "hosts.db"), socket)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cnt := startCounter(t, iface, own)
	l := startDaemon(t, cfgPath, socket)
	l.waitCollector(t, iface, platform.CollectorInterface, platform.StateRunning)
	l.waitCollector(t, iface, platform.CollectorARP, platform.StateRunning)
	ctx := context.Background()

	// The prefix guard refuses a network wider than /24.
	wide, err := l.client.PlanScan(ctx, probe.Request{Networks: []netip.Prefix{netip.MustParsePrefix("172.31.0.0/16")}})
	if err != nil || wide.Allowed || !strings.Contains(strings.Join(wide.Reasons, "; "), "wider than max_auto_scan_prefix_v4") {
		t.Errorf("wide plan = %+v, %v", wide.Reasons, err)
	}

	// 1. An ARP sweep of the whole network makes the hosts known; raw ARP
	// leaves the kernel's neighbour table alone.
	res, err := l.client.Scan(ctx, probe.Request{ARP: true})
	if err != nil {
		t.Fatalf("sweep: %v\n%s", err, l.log)
	}
	if got := res.Interfaces[0].Responders; got < 5 {
		t.Errorf("%d hosts answered the sweep, want at least 5 (open, closed, swap, gone, vanish)", got)
	}

	// 2. The measured scan: every probe on the lower half, all of whose
	// known hosts are up, against the plan's estimate.
	req := probe.Request{Profile: "all", Networks: []netip.Prefix{lower}}
	plan, err := l.client.PlanScan(ctx, req)
	if err != nil || !plan.Allowed {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	start := time.Now()
	res, err = l.client.Scan(ctx, req)
	end := time.Now()
	if err != nil {
		t.Fatalf("scan: %v\n%s", err, l.log)
	}
	time.Sleep(200 * time.Millisecond) // the counter's ring hands over blocks every 10 ms
	var during []frame
	for _, f := range cnt.all() {
		if f.probe() && !f.at.Before(start) && !f.at.After(end) {
			during = append(during, f)
		}
	}
	isStart := starts(during)
	kernel := map[string]int{}
	for i, f := range during {
		if !isStart[i] {
			kernel[f.kind]++
		}
	}
	sent := len(isStart) - countFalse(isStart)
	took, est, planned := end.Sub(start).Seconds(), plan.Estimate.TypicalSeconds, plan.Estimate.Starts
	t.Logf("measured scan: %d probe starts and %d kernel packets %v in %.1f s; plan estimated %d starts, typical %.1f s, no-response %.1f s; %d sweep targets, %d known hosts",
		sent, len(during)-sent, kernel, took, planned, est, plan.Estimate.NoResponseSeconds, plan.Interfaces[0].SweepTargets, plan.Interfaces[0].KnownTargets)
	if math.Abs(float64(sent-planned)) > 0.1*float64(planned) {
		t.Errorf("scan made %d probe starts, plan estimated %d: more than 10%% apart", sent, planned)
	}
	// A TCP check is a bare connect: SYN, the handshake's ACK and the RST,
	// at most 3 packets each; with no loss here the kernel retransmits
	// nothing.
	if n, syn := kernel["tcp"]+kernel["tcp-syn"], plan.Estimate.TCPConnects; n > 2*syn {
		t.Errorf("%d kernel TCP packets for %d connects, want at most 2 each", n, syn)
	}
	if math.Abs(took-est) > 0.1*est {
		t.Errorf("scan took %.1f s, plan estimated %.1f s: more than 10%% apart", took, est)
	}
	counts := res.Interfaces[0].Counts
	if counts["tcp/502"]["OPEN"] != 1 || counts["tcp/502"]["REFUSED"] < 3 || counts["icmp"]["reply"] < 4 {
		t.Errorf("counts = %v", counts)
	}

	// 3. A known host vanishes: its connect is UNREACHABLE, the others are
	// REFUSED.
	request(t, "stop-vanish")
	res, err = l.client.Scan(ctx, probe.Request{TCP: []int{503}})
	if err != nil {
		t.Fatalf("tcp scan: %v", err)
	}
	if c := res.Interfaces[0].Counts["tcp/503"]; c["UNREACHABLE"] != 1 || c["REFUSED"] < 4 {
		t.Errorf("tcp/503 counts = %v", c)
	}
	services, err := l.client.Services(ctx, store.ServiceFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// Results per address and port; the vanished host's address keeps
	// its last definite answer apart from the UNREACHABLE result.
	state := map[string]string{}
	for _, s := range services {
		state[fmt.Sprintf("%s:%d", s.IP, s.Port)] = s.LastResult
		if s.IP == vanish.String() && s.Port == 503 && s.LastDefiniteResult != "" {
			t.Errorf("vanish:503 has a definite answer %s it never gave", s.LastDefiniteResult)
		}
	}
	for k, want := range map[string]string{
		open.String() + ":502": "OPEN", closed.String() + ":502": "REFUSED", swap.String() + ":502": "REFUSED",
		gone.String() + ":502": "REFUSED", vanish.String() + ":503": "UNREACHABLE", open.String() + ":503": "REFUSED",
	} {
		if state[k] != want {
			t.Errorf("service %s = %q, want %s (all: %v)", k, state[k], want, state)
		}
	}

	// 4. The kill switch stops a running sweep at once.
	scanDone := make(chan error, 1)
	var killed scheduler.ScanResult
	scanStart := time.Now()
	go func() {
		var err error
		killed, err = l.client.Scan(ctx, probe.Request{ARP: true})
		scanDone <- err
	}()
	// Stop it once the sweep is on the wire, not after a fixed delay: on a
	// slow runner the switch could land before the scan starts and refuse
	// it instead of stopping it. The sweep takes about 50 s at 5 starts/s.
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		n := 0
		for _, f := range cnt.all() {
			if f.kind == "arp" && f.at.After(scanStart) {
				n++
			}
		}
		if n >= 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the sweep never reached the wire (%d ARP requests)\n%s", n, l.log)
		}
	}
	if _, err := l.client.DisableActive(ctx, "test: stop now"); err != nil {
		t.Fatal(err)
	}
	disabledAt := time.Now()
	if err := <-scanDone; err != nil || len(killed.Interfaces) != 1 || !strings.Contains(killed.Interfaces[0].Aborted, "kill switch") {
		t.Errorf("killed scan = %+v, %v", killed, err)
	}
	time.Sleep(2 * time.Second)
	frames := cnt.all()
	for _, f := range frames {
		if f.probe() && f.at.After(disabledAt.Add(time.Second)) {
			t.Errorf("%s to %s sent %v after the kill switch", f.kind, f.dst, f.at.Sub(disabledAt))
		}
	}

	// Start budgets in every one-second window, from the frames on the
	// wire (the defaults: 5 starts/s in all, ARP 5, ICMP 1, TCP 1). Kernel
	// packets are not starts: they are logged, not budgeted.
	isStart = starts(frames)
	var startFrames []frame
	for i, f := range frames {
		if isStart[i] {
			startFrames = append(startFrames, f)
		}
	}
	for _, c := range []struct {
		name  string
		keep  func(frame) bool
		limit int
	}{
		{"ARP requests", func(f frame) bool { return f.kind == "arp" }, 5},
		{"ICMP echoes", func(f frame) bool { return f.kind == "icmp" }, 1},
		{"TCP connects", func(f frame) bool { return f.kind == "tcp-syn" }, 1},
		{"all probe starts", func(frame) bool { return true }, 5},
	} {
		n, from := maxPerSecond(startFrames, c.keep)
		t.Logf("%s: at most %d in one second", c.name, n)
		if n > c.limit {
			t.Errorf("%s: %d in the second from %s, budget %d", c.name, n, from.Format("15:04:05.000"), c.limit)
			for _, f := range startFrames {
				if c.keep(f) && !f.at.Before(from) && f.at.Before(from.Add(time.Second)) {
					t.Logf("  %s %s %s", f.at.Format("15:04:05.000000"), f.kind, f.dst)
				}
			}
		}
	}
	// Excluded addresses never see a probe, nor do addresses outside the
	// configured network.
	for _, f := range frames {
		if f.probe() && (f.dst == plc || excluded.Contains(f.dst) || f.dst.Is4() && !prefix.Contains(f.dst)) {
			t.Errorf("%s sent to %s at %s", f.kind, f.dst, f.at.Format("15:04:05.000"))
		}
	}
	if n, _ := maxPerSecond(frames, frame.probe); n > 0 {
		t.Logf("all packets including the kernel's: at most %d in one second", n)
	}
	// No payload and no UDP, ever: no banner, TLS, HTTP, Modbus, SNMP, NTP
	// or EtherNet/IP traffic (ADR 0011).
	for _, f := range frames {
		if f.payload() {
			t.Errorf("%s to %s at %s: LAN Tracker sends no payloads", f.kind, f.dst, f.at.Format("15:04:05.000"))
		}
	}
	// Two IP probes to one target are at least the 5 s spacing apart.
	last := map[netip.Addr]time.Time{}
	for i, f := range frames {
		if (f.kind != "icmp" && f.kind != "tcp-syn") || !isStart[i] {
			continue
		}
		if prev, ok := last[f.dst]; ok && f.at.Sub(prev) < 4950*time.Millisecond {
			t.Errorf("%s to %s %v after the previous probe", f.kind, f.dst, f.at.Sub(prev))
		}
		last[f.dst] = f.at
	}
	if t.Failed() {
		t.Logf("daemon log:\n%s", l.log)
	}

	// 5. The kill switch survives a restart, and refuses scans until it is
	// cleared.
	l.stop(t)
	l = startDaemon(t, cfgPath, socket)
	st, err := l.client.Status(ctx)
	if err != nil || !st.Active.Disabled || st.Active.Reason != "test: stop now" {
		t.Errorf("after restart: %+v, %v", st.Active, err)
	}
	if _, err := l.client.Scan(ctx, probe.Request{ARP: true, Networks: []netip.Prefix{netip.PrefixFrom(open, 32)}}); !errors.Is(err, scheduler.ErrScanRefused) {
		t.Errorf("scan with the switch set: %v", err)
	}
	if _, err := l.client.EnableActive(ctx, "test done"); err != nil {
		t.Fatal(err)
	}
	if p, err := l.client.PlanScan(ctx, probe.Request{}); err != nil || !p.Allowed {
		t.Errorf("plan after enable: %+v, %v", p.Reasons, err)
	}
	l.stop(t)
}

func countFalse(m map[int]bool) int {
	n := 0
	for _, v := range m {
		if !v {
			n++
		}
	}
	return n
}

// TestICMPRawSocket runs where ping sockets are not allowed for the
// service user (run.sh sets net.ipv4.ping_group_range to "1 0"): the
// transmitter falls back to a raw ICMP socket under CAP_NET_RAW, and echo
// replies still come back.
func TestICMPRawSocket(t *testing.T) {
	if os.Getenv("LT_TEST_RAW_ICMP") != "1" {
		t.Skip("run by run.sh with ping sockets disabled")
	}
	iface, open := env(t, "LT_TEST_IFACE"), ip(t, "LT_TEST_OPEN")
	ctx := context.Background()
	tx := platform.New().Transmitter
	conn, err := tx.ICMPConn(ctx, iface)
	if err != nil {
		t.Fatal(err)
	}
	ping := conn.Ping()
	_ = conn.Close()
	if ping {
		t.Fatal("got a ping socket although ping_group_range excludes this user")
	}
	var mu sync.Mutex
	var got []string
	pass := probe.Pass{
		Link:    probe.Link{Name: iface, MAC: ownMAC(t, iface)},
		Targets: []netip.Addr{open, ip(t, "LT_TEST_ABSENT")},
		Budget:  probe.NewBudget(nil, probe.Limits{MaxConcurrent: 4}, nil),
		Emit: func(o observation.Observation) {
			mu.Lock()
			got = append(got, string(o.Source)+" "+o.IP.String())
			mu.Unlock()
		},
	}
	results, err := icmp.Engine{TX: tx}.Run(ctx, pass)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].State != probe.Reply || results[1].State != probe.NoReply {
		t.Errorf("results = %+v", results)
	}
	if len(got) != 1 || got[0] != "icmp_scan "+open.String() {
		t.Errorf("observations = %v", got)
	}
}
