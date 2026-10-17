package daemon

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

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

// The parallel suite looks for shared state and lost updates: several
// daemons replaying the same capture at once must agree exactly, and one
// daemon serving many concurrent operators must apply every write, run one
// scan at a time and answer every reader. Run it with -race (make test,
// make chaos).

// replayDump runs one replay of the site capture and summarises what it
// recorded, without the random host IDs.
func replayDump(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	pcap, err := filepath.Abs("../../test/fixtures/site-a-eth1.pcapng")
	if err != nil {
		t.Fatal(err)
	}
	cfgPath, db := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "hosts.db")
	cfg := "version: 1\ninterfaces:\n  - name: eth1\n    prefixes: [192.168.110.0/24]\n    dhcp: { servers: [192.168.110.2] }\n" +
		"    replay: { file: " + pcap + ", speed: 0 }\nreplay: { exit_when_done: true }\nstorage: { path: " + db +
		" }\napi: { socket: " + filepath.Join(dir, "api.sock") + " }\nlogging: { format: text }\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	log := &syncBuffer{}
	backends, _, _, _, _ := fake.Backends()
	errc := make(chan error, 1)
	go func() {
		errc <- Run(context.Background(), Options{Load: config.LoadOptions{Path: cfgPath}, Stderr: log, Backends: &backends,
			Notifier: &fakeNotifier{}, Signals: make(chan os.Signal)})
	}()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, log)
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("replay did not finish\n%s", log)
	}
	var parts []string
	for _, q := range []string{
		`SELECT mac || ' ' || presence || ' ' || coalesce(preferred_name, '') FROM hosts ORDER BY mac`,
		`SELECT h.mac || ' ' || a.ip || ' ' || a.first_seen || ' ' || coalesce(a.ended_at, '') || ' ' || a.conflict
			FROM addresses a JOIN hosts h ON h.host_id = a.host_id ORDER BY h.mac, a.ip, a.first_seen`,
		`SELECT e.ts || ' ' || e.type || ' ' || coalesce(h.mac, '') || ' ' || coalesce(e.old_value, '') || '>' || coalesce(e.new_value, '')
			FROM events e LEFT JOIN hosts h ON h.host_id = e.host_id ORDER BY e.id`,
		`SELECT chaddr || ' ' || ip || ' ' || first_ack || ' ' || coalesce(ended_at, '') FROM dhcp_leases ORDER BY id`,
		`SELECT name || ' ' || record_type || ' ' || coalesce(address, '') || ' ' || first_seen || ' ' || link FROM name_records ORDER BY id`,
		`SELECT server_id || ' ' || mac || ' ' || status || ' ' || config_json FROM dhcp_servers ORDER BY id`,
	} {
		parts = append(parts, strings.Join(queryDB(t, db, q), "\n"))
	}
	return strings.Join(parts, "\n--\n")
}

// Daemons replaying the same capture at the same time record exactly the
// same history: no state is shared between instances, and a replay does
// not depend on scheduling.
func TestParallelReplays(t *testing.T) {
	const n = 6
	dumps := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dumps[i] = replayDump(t)
		}()
	}
	wg.Wait()
	if !strings.Contains(dumps[0], "DHCP_LEASE_ASSIGNED") || !strings.Contains(dumps[0], "HOST_DISCOVERED") {
		t.Fatalf("the replay recorded too little:\n%s", dumps[0])
	}
	for i := 1; i < n; i++ {
		if dumps[i] != dumps[0] {
			t.Fatalf("replay %d differs from replay 0:\n%s\n----\n%s", i, dumps[i], dumps[0])
		}
	}
}

// One daemon, many operators at once: every description write lands and
// the last one wins per host, scans never overlap, reloads and the kill
// switch serialise, readers online and offline always get an answer.
func TestParallelOperators(t *testing.T) {
	dir := t.TempDir()
	cfgPath, dbPath, socket := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "hosts.db"), filepath.Join(dir, "api.sock")
	cfg := fmt.Sprintf("version: 1\ninterfaces:\n  - name: eth1\n    active: { enabled: true, networks: [10.30.0.0/28] }\n"+
		"active: { startup_delay: 24h }\nstorage: { path: %s }\napi: { socket: %s }\nlogging: { format: text }\n", dbPath, socket)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	backends, capt, _, ifaces, ftx := fake.Backends()
	ifaces.SetLinks([]platform.Link{{Name: "eth1", Index: 2, Up: true, MAC: net.HardwareAddr{2, 0, 0, 0, 3, 1},
		Prefixes: []netip.Prefix{netip.MustParsePrefix("10.30.0.0/24")}, Addrs: []netip.Prefix{netip.MustParsePrefix("10.30.0.250/24")}}})
	ftx.FrameReply = arpNetwork(map[string]net.HardwareAddr{"10.30.0.3": frames.MAC("00:1b:1b:00:03:03")})
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
		t.Fatal("daemon not ready")
	}
	client := api.NewClient(socket, 20*time.Second)
	ctx := context.Background()

	// Twelve hosts announce themselves.
	const hosts = 12
	for capt.Source("eth1") == nil {
		time.Sleep(5 * time.Millisecond)
	}
	for i := range hosts {
		mac := net.HardwareAddr{0x00, 0x1b, 0x1b, 0x00, 0x04, byte(i)}
		capt.Source("eth1").Inject(time.Now(), frames.ARP(layers.ARPRequest, mac, fmt.Sprintf("10.30.0.%d", 20+i), frames.Broadcast,
			fmt.Sprintf("10.30.0.%d", 20+i)))
	}
	var list []store.HostSummary
	deadline := time.Now().Add(10 * time.Second)
	for len(list) < hosts && time.Now().Before(deadline) {
		list, _ = client.Hosts(ctx, store.HostFilter{})
		time.Sleep(20 * time.Millisecond)
	}
	if len(list) < hosts {
		t.Fatalf("%d hosts, want %d", len(list), hosts)
	}

	const writes = 15
	errs := make(chan error, 1000)
	var wg sync.WaitGroup
	goN := func(n int, fn func(i int)) {
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				fn(i)
			}()
		}
	}
	goN(hosts, func(i int) {
		for k := range writes {
			if _, err := client.SetDescription(ctx, list[i].HostID, fmt.Sprintf("host %d write %d", i, k)); err != nil {
				errs <- fmt.Errorf("describe %d/%d: %w", i, k, err)
			}
		}
	})
	goN(3, func(int) {
		for range 10 {
			if _, err := client.DisableActive(ctx, "parallel"); err != nil {
				errs <- fmt.Errorf("disable: %w", err)
			}
			if _, err := client.EnableActive(ctx, "parallel"); err != nil {
				errs <- fmt.Errorf("enable: %w", err)
			}
		}
	})
	goN(4, func(int) {
		for range 4 {
			_, err := client.Scan(ctx, probe.Request{ARP: true})
			// Another scan running, or the plan refused while another
			// operator holds the kill switch set, are the only answers
			// besides a result.
			if err != nil && !strings.Contains(err.Error(), "already running") && !strings.Contains(err.Error(), "refused") {
				errs <- fmt.Errorf("scan: %w", err)
			}
		}
	})
	goN(3, func(int) {
		for range 8 {
			if _, err := client.ReloadConfig(ctx); err != nil {
				errs <- fmt.Errorf("reload: %w", err)
			}
		}
	})
	goN(4, func(int) {
		for range 30 {
			if _, err := client.Hosts(ctx, store.HostFilter{}); err != nil {
				errs <- fmt.Errorf("hosts: %w", err)
			}
			if _, err := client.Events(ctx, store.EventFilter{Limit: 20}); err != nil {
				errs <- fmt.Errorf("events: %w", err)
			}
		}
	})
	goN(3, func(int) {
		r, err := store.OpenReadOnly(dbPath, store.ReadOptions{})
		if err != nil {
			errs <- fmt.Errorf("offline open: %w", err)
			return
		}
		defer r.Close()
		for range 30 {
			if _, err := r.Hosts(ctx, store.HostFilter{}); err != nil {
				errs <- fmt.Errorf("offline hosts: %w", err)
			}
		}
	})
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// Every host's last description is its last write, and every write
	// that changed the text is one HOST_DESCRIBED event.
	for i := range hosts {
		h, err := client.Host(ctx, list[i].HostID)
		if err != nil || h.Description != fmt.Sprintf("host %d write %d", i, writes-1) {
			t.Errorf("host %d: description %q, %v", i, h.Description, err)
		}
	}
	described, err := client.Events(ctx, store.EventFilter{Types: []string{"HOST_DESCRIBED"}})
	if err != nil || len(described) != hosts*writes {
		t.Errorf("%d HOST_DESCRIBED events, want %d (%v)", len(described), hosts*writes, err)
	}
	if st, err := client.Status(ctx); err != nil || st.Active.Disabled {
		t.Errorf("kill switch after the last enable: %+v, %v", st.Active, err)
	}
	// With the kill switch clear and nothing else running, a scan runs.
	if _, err := client.Scan(ctx, probe.Request{ARP: true}); err != nil {
		t.Errorf("scan after the concurrent phase: %v", err)
	}
	signals <- syscall.SIGTERM
	if err := <-errc; err != nil {
		t.Fatalf("daemon exit: %v", err)
	}
	if n := d.store.OpErrors(); n != 0 {
		t.Errorf("%d store writes failed\n%s", n, tail(log.String()))
	}
	// Scans on one interface never overlap.
	rows := queryDB(t, dbPath, `SELECT started_at || ' ' || coalesce(finished_at, '') FROM scans ORDER BY started_at, id`)
	var lastEnd int64
	for _, r := range rows {
		f := strings.Fields(r)
		if len(f) != 2 {
			t.Errorf("scan left open: %q", r)
			continue
		}
		start, _ := strconv.ParseInt(f[0], 10, 64)
		end, _ := strconv.ParseInt(f[1], 10, 64)
		if start < lastEnd {
			t.Errorf("scans overlap: %v", rows)
			break
		}
		lastEnd = end
	}
	if len(rows) == 0 {
		t.Error("no scan ran")
	}
}
