package daemon

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"

	"github.com/otfabric/lan-tracker/internal/api"
	"github.com/otfabric/lan-tracker/internal/clock"
	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/platform/fake"
	"github.com/otfabric/lan-tracker/internal/probe"
	"github.com/otfabric/lan-tracker/internal/store"
	"github.com/otfabric/lan-tracker/test/frames"
)

// The daemon chaos test runs the whole daemon on the real clock with fake
// platform backends, and for a while does everything at once: floods the
// captures with valid, malformed and truncated frames, takes collectors
// and links down and up, answers probes erratically, and through the API
// reads everything, describes hosts, sets and clears the kill switch,
// starts scans and reloads valid and invalid configurations. Then it shuts
// the daemon down and checks what must hold whatever happened: a clean
// exit, no failed store write, a sound database, every one-second window
// of probe starts within the budgets, nothing sent while the kill switch
// was set, and no goroutine left behind. LT_CHAOS_DURATION (e.g. 60s) and
// LT_CHAOS_SEED make longer and reproducible runs (make chaos).

// sends records when the daemon sent each probe, by protocol.
type sends struct {
	mu sync.Mutex
	at map[probe.Protocol][]time.Time
}

func (s *sends) add(p probe.Protocol) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at[p] = append(s.at[p], time.Now())
}

func (s *sends) all() map[probe.Protocol][]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[probe.Protocol][]time.Time{}
	for p, ts := range s.at {
		out[p] = slices.Clone(ts)
	}
	return out
}

// timedTX wraps the fake transmitter and records every probe start.
type timedTX struct {
	*fake.Transmitter
	s *sends
}

func (t timedTX) Frames(ctx context.Context, iface string, et uint16) (platform.FrameConn, error) {
	c, err := t.Transmitter.Frames(ctx, iface, et)
	if err != nil {
		return nil, err
	}
	return timedFrames{c, t.s}, nil
}

func (t timedTX) DialTCP(ctx context.Context, iface string, addr netip.AddrPort, timeout time.Duration) (net.Conn, error) {
	t.s.add(probe.TCP)
	return t.Transmitter.DialTCP(ctx, iface, addr, timeout)
}

func (t timedTX) DialUDP(ctx context.Context, iface string, addr netip.AddrPort) (net.Conn, error) {
	c, err := t.Transmitter.DialUDP(ctx, iface, addr)
	if err != nil {
		return nil, err
	}
	return timedUDP{c, t.s}, nil
}

func (t timedTX) ListenUDP(ctx context.Context, iface string) (net.PacketConn, error) {
	c, err := t.Transmitter.ListenUDP(ctx, iface)
	if err != nil {
		return nil, err
	}
	return timedPacket{c, t.s}, nil
}

func (t timedTX) ICMPConn(ctx context.Context, iface string) (platform.EchoConn, error) {
	c, err := t.Transmitter.ICMPConn(ctx, iface)
	if err != nil {
		return nil, err
	}
	return timedEcho{c, t.s}, nil
}

type timedFrames struct {
	platform.FrameConn
	s *sends
}

func (c timedFrames) WriteFrame(ctx context.Context, f []byte) error {
	c.s.add(probe.ARP)
	return c.FrameConn.WriteFrame(ctx, f)
}

type timedUDP struct {
	net.Conn
	s *sends
}

func (c timedUDP) Write(p []byte) (int, error) {
	c.s.add(probe.Name)
	return c.Conn.Write(p)
}

type timedPacket struct {
	net.PacketConn
	s *sends
}

func (c timedPacket) WriteTo(p []byte, a net.Addr) (int, error) {
	c.s.add(probe.Name)
	return c.PacketConn.WriteTo(p, a)
}

type timedEcho struct {
	platform.EchoConn
	s *sends
}

func (c timedEcho) WriteTo(msg []byte, dst netip.Addr) error {
	c.s.add(probe.ICMP)
	return c.EchoConn.WriteTo(msg, dst)
}

// lockedRand is a seeded source safe for the responders' goroutines.
type lockedRand struct {
	mu sync.Mutex
	r  *rand.Rand
}

func (l *lockedRand) IntN(n int) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.r.IntN(n)
}

func echoReply(msg []byte) []byte {
	if len(msg) < 8 {
		return nil
	}
	r := slices.Clone(msg)
	r[0], r[2], r[3] = 0, 0, 0 // echo reply, checksum recomputed
	var sum uint32
	for i := 0; i+1 < len(r); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(r[i:]))
	}
	if len(r)%2 == 1 {
		sum += uint32(r[len(r)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	binary.BigEndian.PutUint16(r[2:], ^uint16(sum))
	return r
}

const chaosConfig = `version: 1
interfaces:
  - name: eth1
    dhcp: { servers: [10.20.1.2] }
    active: { enabled: true, networks: [10.20.1.0/24], exclude: [10.20.1.1] }
  - name: eth2
    active: { enabled: %t, networks: [10.20.2.0/28] }
active:
  startup_delay: 0s
  jitter: 0
  arp: { enabled: true, interval: %ds }
  icmp: { enabled: true, interval: 10s }
  tcp: { enabled: true, interval: 10s, targets: [{ port: 502, timeout: 750ms }, { port: %d, timeout: 750ms }] }
  names: { enabled: true, interval: 10s, service_types: [_http._tcp], resolvers: [10.20.1.2] }
storage: { path: %s }
api: { socket: %s }
logging: { level: info, format: text }
`

func TestChaosDaemon(t *testing.T) {
	duration := 4 * time.Second
	if d, err := time.ParseDuration(os.Getenv("LT_CHAOS_DURATION")); err == nil && d > 0 {
		duration = d
	}
	seed := uint64(time.Now().UnixNano())
	if v, err := strconv.ParseUint(os.Getenv("LT_CHAOS_SEED"), 10, 64); err == nil && v != 0 {
		seed = v
	}
	t.Logf("seed %d, %s", seed, duration)
	rnd := &lockedRand{r: rand.New(rand.NewPCG(seed, seed^0x5851f42d4c957f2d))}
	goroutines := runtime.NumGoroutine()

	dir := t.TempDir()
	cfgPath, dbPath, socket := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "hosts.db"), filepath.Join(dir, "api.sock")
	writeCfg := func(eth2 bool, arpInterval, port int) {
		cfg := fmt.Sprintf(chaosConfig, eth2, arpInterval, port, dbPath, socket)
		if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeCfg(true, 10, 80)

	backends, capt, neigh, ifaces, ftx := fake.Backends()
	rec := &sends{at: map[probe.Protocol][]time.Time{}}
	backends.Transmitter = timedTX{ftx, rec}
	macOf := func(ip netip.Addr) net.HardwareAddr {
		a := ip.As4()
		return net.HardwareAddr{0x00, 0x1b, 0x1b, a[2], a[3], byte(rnd.IntN(2))} // two MACs per address: conflicts
	}
	ftx.FrameReply = func(_ string, frame []byte) [][]byte {
		p := gopacket.NewPacket(frame, layers.LayerTypeEthernet, gopacket.Default)
		a, _ := p.Layer(layers.LayerTypeARP).(*layers.ARP)
		if a == nil || a.Operation != layers.ARPRequest || rnd.IntN(3) == 0 {
			return nil
		}
		target := netip.AddrFrom4([4]byte(a.DstProtAddress))
		return [][]byte{frames.ARP(layers.ARPReply, macOf(target), target.String(), a.SourceHwAddress,
			netip.AddrFrom4([4]byte(a.SourceProtAddress)).String())}
	}
	ftx.EchoReply = func(_ netip.Addr, msg []byte) []byte {
		if rnd.IntN(2) == 0 {
			return nil
		}
		return echoReply(msg)
	}
	ftx.UDPReply = func(_ netip.AddrPort, q []byte) []byte {
		switch rnd.IntN(3) {
		case 0:
			return nil
		case 1:
			return []byte("not dns")
		}
		return q // the question echoed back: a malformed answer
	}
	tcpErrs := []error{nil, syscall.ECONNREFUSED, syscall.ETIMEDOUT, syscall.EHOSTUNREACH, syscall.EPERM}
	ftx.TCPResult = map[netip.AddrPort]error{}
	for i := 1; i < 32; i++ {
		for _, port := range []uint16{80, 502, 503} {
			ftx.TCPResult[netip.AddrPortFrom(netip.AddrFrom4([4]byte{10, 20, 1, byte(i)}), port)] = tcpErrs[rnd.IntN(len(tcpErrs))]
		}
	}
	links := func(up bool) []platform.Link {
		return []platform.Link{
			{Name: "eth1", Index: 2, Up: up, MAC: net.HardwareAddr{2, 0, 0, 0, 1, 1},
				Prefixes: []netip.Prefix{netip.MustParsePrefix("10.20.1.0/24")}, Addrs: []netip.Prefix{netip.MustParsePrefix("10.20.1.250/24")}},
			{Name: "eth2", Index: 3, Up: true, MAC: net.HardwareAddr{2, 0, 0, 0, 2, 1},
				Prefixes: []netip.Prefix{netip.MustParsePrefix("10.20.2.0/24")}, Addrs: []netip.Prefix{netip.MustParsePrefix("10.20.2.250/24")}},
		}
	}
	ifaces.SetLinks(links(true))

	log := &syncBuffer{}
	signals := make(chan os.Signal, 1)
	d, err := New(Options{Load: config.LoadOptions{Path: cfgPath}, Stderr: log, Clock: clock.Real(), Backends: &backends,
		Notifier: &fakeNotifier{}, Signals: signals})
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- d.Run(context.Background()) }()
	select {
	case <-d.Ready():
	case err := <-errc:
		t.Fatalf("daemon: %v\n%s", err, log)
	case <-time.After(15 * time.Second):
		t.Fatalf("daemon not ready\n%s", log)
	}
	client := api.NewClient(socket, 10*time.Second)

	// Kill switch windows: from the moment a disable returned to the
	// moment the enable was asked for, nothing may be sent.
	var swMu sync.Mutex
	type window struct{ from, to time.Time }
	var disabled []window

	stop := make(chan struct{})
	var wg sync.WaitGroup
	run := func(name string, every time.Duration, fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				case <-time.After(every):
					fn()
				}
			}
		}()
	}
	ctx := context.Background()
	ifaceOf := func() string { return []string{"eth1", "eth2"}[rnd.IntN(2)] }
	addr := func() string { return fmt.Sprintf("10.20.1.%d", 2+rnd.IntN(28)) }
	hosts := []net.HardwareAddr{frames.MAC("00:1b:1b:00:01:01"), frames.MAC("00:1b:1b:00:01:02"), frames.MAC("00:0e:8c:00:01:03"),
		frames.MAC("02:42:ac:00:01:04")}
	server := frames.MAC("00:15:5d:00:01:02")

	// Capture: valid frames of every decoded kind, and garbage.
	run("frames", time.Millisecond, func() {
		src := capt.Source(ifaceOf())
		if src == nil {
			return
		}
		h := hosts[rnd.IntN(len(hosts))]
		var f []byte
		switch rnd.IntN(9) {
		case 0:
			f = frames.ARP(layers.ARPRequest, h, addr(), frames.Broadcast, addr())
		case 1:
			f = frames.ARP(layers.ARPReply, h, addr(), hosts[0], addr())
		case 2:
			f = frames.DHCP{Type: layers.DHCPMsgTypeDiscover, Client: h, Hostname: "HMI-" + strconv.Itoa(rnd.IntN(3)),
				XID: uint32(rnd.IntN(4))}.Frame()
		case 3:
			f = frames.DHCP{Type: layers.DHCPMsgTypeAck, Client: h, YourIP: addr(), Server: server, ServerIP: "10.20.1.2",
				ServerID: "10.20.1.2", Router: []string{"10.20.1.1"}, LeaseSeconds: uint32(60 + rnd.IntN(600)), XID: uint32(rnd.IntN(4))}.Frame()
		case 4:
			ip := addr()
			f = frames.MDNS4(h, ip, []layers.DNSResourceRecord{frames.A(fmt.Sprintf("dev-%d.local", rnd.IntN(4)), ip)}, nil)
		case 5:
			f = frames.ICMPv4Echo(h, hosts[0], addr(), addr())
		case 6:
			f = frames.LLDP{Source: h, ChassisMAC: h, PortID: "p" + strconv.Itoa(rnd.IntN(4)), TTL: 120, SystemName: "sw"}.Frame()
		case 7:
			f = frames.ARP(layers.ARPReply, h, addr(), hosts[0], addr())
			f = f[:rnd.IntN(len(f))] // truncated
		default:
			f = make([]byte, rnd.IntN(200))
			for i := range f {
				f[i] = byte(rnd.IntN(256))
			}
		}
		src.Inject(time.Now(), f)
	})
	// Collectors and links failing and recovering; neighbour events.
	run("collectors", 150*time.Millisecond, func() {
		switch rnd.IntN(16) { // link flaps are rare: probes pause while a link is down
		case 0:
			if src := capt.Source(ifaceOf()); src != nil {
				src.LinkDown()
			}
		case 1:
			capt.SetOpenErr(syscall.EPERM)
		case 2:
			capt.SetOpenErr(nil)
		case 3:
			up := rnd.IntN(3) != 0
			ifaces.SetLinks(links(up))
			ifaces.Emit(platform.LinkEvent{Link: links(up)[0]})
		default:
			neigh.Emit(platform.NeighborEvent{Neighbor: platform.Neighbor{Interface: ifaceOf(), IP: netip.MustParseAddr(addr()),
				MAC: hosts[rnd.IntN(len(hosts))], State: []string{"REACHABLE", "STALE", "FAILED"}[rnd.IntN(3)]}, Deleted: rnd.IntN(4) == 0})
		}
	})
	// Readers.
	for i := range 3 {
		run("reader"+strconv.Itoa(i), 20*time.Millisecond, func() {
			switch rnd.IntN(8) {
			case 0:
				_, _ = client.Hosts(ctx, store.HostFilter{})
			case 1:
				_, _ = client.Find(ctx, store.FindQuery{Query: addr(), At: ptr(time.Now().Add(-time.Second))})
			case 2:
				_, _ = client.Services(ctx, store.ServiceFilter{})
			case 3:
				_, _ = client.DHCPLeases(ctx, store.DHCPLeaseFilter{})
			case 4:
				_, _ = client.Events(ctx, store.EventFilter{Limit: 50})
			case 5:
				_, _ = client.Status(ctx)
			case 6:
				_, _ = client.DHCPExchanges(ctx, store.DHCPExchangeFilter{})
			default:
				_, _ = client.DBCheck(ctx)
			}
		})
	}
	// Operators.
	run("describe", 100*time.Millisecond, func() {
		if hs, err := client.Hosts(ctx, store.HostFilter{}); err == nil && len(hs) > 0 {
			_, _ = client.SetDescription(ctx, hs[rnd.IntN(len(hs))].HostID, "chaos "+strconv.Itoa(rnd.IntN(100)))
		}
	})
	run("killswitch", 3*time.Second, func() {
		if _, err := client.DisableActive(ctx, "chaos"); err != nil {
			return
		}
		from := time.Now()
		time.Sleep(time.Duration(50+rnd.IntN(250)) * time.Millisecond)
		to := time.Now()
		if _, err := client.EnableActive(ctx, "chaos"); err != nil {
			t.Errorf("enable: %v", err)
		}
		swMu.Lock()
		disabled = append(disabled, window{from, to})
		swMu.Unlock()
	})
	var scanMu sync.Mutex
	scanOutcomes := map[string]int{}
	run("scan", 200*time.Millisecond, func() {
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		_, err := client.Scan(sctx, probe.Request{Interfaces: []string{"eth1"}, ARP: true, ICMP: true, TCP: []int{502}, Networks: []netip.Prefix{netip.MustParsePrefix("10.20.1.0/26")}})
		scanMu.Lock()
		if err == nil {
			scanOutcomes["ok"]++
		} else {
			scanOutcomes[err.Error()]++
		}
		scanMu.Unlock()
	})
	run("reload", time.Second, func() {
		if rnd.IntN(4) == 0 {
			if err := os.WriteFile(cfgPath, []byte("version: 1\ninterfaces: []\nactive: { jitter: 2 }\n"), 0o600); err != nil {
				t.Error(err)
			}
			if _, err := client.ReloadConfig(ctx); err == nil {
				t.Error("an invalid configuration was applied")
			}
		}
		writeCfg(rnd.IntN(2) == 0, 10+rnd.IntN(20), []int{80, 503}[rnd.IntN(2)])
		_, _ = client.ReloadConfig(ctx)
	})
	// A stream subscriber that comes and goes.
	run("stream", 500*time.Millisecond, func() {
		sctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		_ = client.Stream(sctx, "", nil, 0, func(store.Event) error { return nil })
	})

	time.Sleep(duration)
	close(stop)
	wg.Wait()
	signals <- syscall.SIGTERM
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("daemon exit: %v\n%s", err, tail(log.String()))
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("daemon did not stop\n%s", tail(log.String()))
	}
	stopped := time.Now()

	if n := d.store.OpErrors(); n != 0 {
		t.Errorf("%d store writes failed\n%s", n, tail(log.String()))
	}
	for _, bad := range []string{"panic", "store: op failed", "commit failed", "DATA RACE"} {
		if strings.Contains(log.String(), bad) {
			t.Errorf("log contains %q\n%s", bad, tail(log.String()))
		}
	}

	// The budgets: no 0.9 s window holds more starts than allowed (the
	// budget enforces them over 1.02 s windows; the margin absorbs the
	// scheduling delay between the budget and the fake's clock).
	limits := map[probe.Protocol]int{probe.ARP: 5, probe.ICMP: 1, probe.TCP: 1, probe.Name: 1}
	all := rec.all()
	var every []time.Time
	for p, ts := range all {
		if most, at := mostIn(ts, 900*time.Millisecond); most > limits[p] {
			t.Errorf("%d %s starts in 0.9 s from %s, budget %d/s", most, p, at.Format(time.RFC3339Nano), limits[p])
		}
		every = append(every, ts...)
		for _, ts := range ts {
			if ts.After(stopped) {
				t.Errorf("%s sent after shutdown", p)
			}
		}
	}
	slices.SortFunc(every, func(a, b time.Time) int { return a.Compare(b) })
	if most, at := mostIn(every, 900*time.Millisecond); most > 5 {
		t.Errorf("%d starts in 0.9 s from %s, global budget 5/s", most, at.Format(time.RFC3339Nano))
	}
	swMu.Lock()
	for _, w := range disabled {
		for p, ts := range all {
			for _, s := range ts {
				if s.After(w.from.Add(20*time.Millisecond)) && s.Before(w.to) {
					t.Errorf("%s sent at %s while the kill switch was set (%s–%s)", p, s.Format(time.RFC3339Nano),
						w.from.Format(time.RFC3339Nano), w.to.Format(time.RFC3339Nano))
				}
			}
		}
	}
	swMu.Unlock()
	t.Logf("scans: %v", scanOutcomes)
	t.Logf("starts: arp %d, icmp %d, tcp %d, names %d; %d kill switch windows", len(all[probe.ARP]), len(all[probe.ICMP]),
		len(all[probe.TCP]), len(all[probe.Name]), len(disabled))
	// The budget checks mean something only under pressure: the sweeps must
	// have run near their rate for a good part of the time.
	if n := len(all[probe.ARP]); float64(n) < duration.Seconds() {
		t.Errorf("only %d ARP starts in %s: the chaos left the budget idle", n, duration)
	}

	// The database is sound, and the correlator's invariants hold on it.
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for q, want := range map[string]string{
		`SELECT integrity_check FROM pragma_integrity_check`: "ok",
		`SELECT count(*) FROM pragma_foreign_key_check`:      "0",
		`SELECT count(*) FROM addresses a WHERE a.ended_at IS NULL AND a.conflict = 0 AND (SELECT count(*) FROM addresses b
			WHERE b.ended_at IS NULL AND b.context_id = a.context_id AND b.ip = a.ip) > 1`: "0",
		`SELECT count(*) FROM addresses a WHERE a.ended_at IS NULL AND a.conflict = 1 AND (SELECT count(*) FROM addresses b
			WHERE b.ended_at IS NULL AND b.context_id = a.context_id AND b.ip = a.ip) < 2`: "0",
		`SELECT count(*) FROM (SELECT 1 FROM dhcp_leases WHERE ended_at IS NULL GROUP BY context_id, chaddr HAVING count(*) > 1)`: "0",
		`SELECT count(*) FROM scans WHERE finished_at IS NULL`:                                                                    "0",
		`SELECT count(*) > 0 FROM hosts`: "1",
	} {
		var got string
		if err := db.QueryRow(q).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", strings.Join(strings.Fields(q), " "), got, err, want)
		}
	}

	// No goroutine outlives the daemon.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > goroutines+2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > goroutines+2 {
		buf := make([]byte, 1<<20)
		t.Errorf("%d goroutines after shutdown, %d before\n%s", n, goroutines, buf[:runtime.Stack(buf, true)])
	}
}

func ptr[T any](v T) *T { return &v }

// mostIn is the most timestamps in any window of length w, and where it
// starts; ts must be sorted.
func mostIn(ts []time.Time, w time.Duration) (int, time.Time) {
	slices.SortFunc(ts, func(a, b time.Time) int { return a.Compare(b) })
	most, at, j := 0, time.Time{}, 0
	for i := range ts {
		for ts[i].Sub(ts[j]) >= w {
			j++
		}
		if n := i - j + 1; n > most {
			most, at = n, ts[j]
		}
	}
	return most, at
}

func tail(s string) string {
	if len(s) > 6000 {
		return "…" + s[len(s)-6000:]
	}
	return s
}
