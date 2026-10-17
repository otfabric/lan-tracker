//go:build nettest

package nettest

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/probe"
)

// TestNameQueriesOnTheWire: with name queries enabled (FR-AC-12), the only
// UDP that leaves the box is one-shot mDNS to 224.0.0.251:5353 and DNS to
// the configured resolver's port 53, at most one query a second (the names
// budget), at most max_queries_per_pass per pass, and nothing else: no
// payload, no other UDP. Nobody answers here (the resolver is a host
// without a DNS server), so every query is one start and no more.
func TestNameQueriesOnTheWire(t *testing.T) {
	iface, prefix := env(t, "LS_TEST_IFACE"), netip.MustParsePrefix(env(t, "LS_TEST_PREFIX"))
	resolver := ip(t, "LS_TEST_CLOSED")
	own := ownMAC(t, iface)
	dir := t.TempDir()
	cfgPath, socket := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "api.sock")
	const perPass = 6
	cfg := fmt.Sprintf(`version: 2
interfaces:
  - name: %s
    passive: { enabled: false }
    active:
      enabled: true
      networks: [%s]
active:
  startup_delay: 1s
  jitter: 0
  arp: { enabled: false }
  icmp: { enabled: false }
  tcp: { enabled: false }
  names:
    enabled: true
    interval: 10s
    mdns: true
    service_types: [_http._tcp, _modbus._tcp]
    enumerate_services: true
    resolvers: [%s]
    max_queries_per_pass: %d
storage: { path: %s }
api: { socket: %s }
logging: { format: text }
`, iface, prefix, resolver, perPass, filepath.Join(dir, "hosts.db"), socket)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cnt := startCounter(t, iface, own)
	l := startDaemon(t, cfgPath, socket)
	defer l.stop(t)
	l.waitCollector(t, iface, platform.CollectorInterface, platform.StateRunning)
	// A sweep of the resolver's /29 makes it (at least) known, so its
	// address is asked about (PTR).
	sweep := probe.Request{ARP: true, Networks: []netip.Prefix{netip.PrefixFrom(resolver, 29).Masked()}}
	if _, err := l.client.Scan(context.Background(), sweep); err != nil {
		t.Fatalf("sweep: %v\n%s", err, l.log)
	}
	start := time.Now()
	time.Sleep(25 * time.Second) // two passes or more
	time.Sleep(200 * time.Millisecond)

	var udp []frame
	mdns, dns := 0, 0
	for _, f := range cnt.all() {
		switch {
		case f.kind == "tcp-data":
			t.Errorf("payload sent: %+v", f)
		case f.kind != "udp":
		case f.dst == netip.MustParseAddr("224.0.0.251") && f.dport == 5353:
			mdns++
			udp = append(udp, f)
		case f.dst == resolver && f.dport == 53:
			dns++
			udp = append(udp, f)
		default:
			t.Errorf("UDP to %s:%d: only the mDNS group and the configured resolver may be asked", f.dst, f.dport)
		}
	}
	t.Logf("name queries in %.0f s: %d mDNS, %d DNS to %s", time.Since(start).Seconds(), mdns, dns, resolver)
	// Per pass: the enumeration and two service types by mDNS, then PTR
	// for the known addresses through the resolver, cut at perPass.
	if mdns < 6 || dns < 2 {
		t.Errorf("%d mDNS and %d DNS queries, want at least two passes' worth (3 mDNS and a PTR each)", mdns, dns)
	}
	if most, at := maxPerSecond(udp, func(frame) bool { return true }); most > 2 {
		t.Errorf("%d name queries in the second from %s: the names budget is 1/s", most, at.Format(time.RFC3339Nano))
	}
	// Passes are 10 s apart: no 10-second window holds more than one pass.
	for i, f := range udp {
		n := 0
		for _, g := range udp[i:] {
			if g.at.After(f.at.Add(9 * time.Second)) {
				break
			}
			n++
		}
		if n > perPass {
			t.Errorf("%d queries within 9 s from %s: more than max_queries_per_pass (%d)", n, f.at.Format(time.RFC3339Nano), perPass)
			break
		}
	}
}
