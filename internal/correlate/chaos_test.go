package correlate

import (
	"database/sql"
	"fmt"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform"
)

// The chaos test feeds the correlator seeded random streams over a small
// pool of MACs and addresses, so that every rule meets every other: ARP,
// passive traffic, neighbour entries, probe results, names, DHCP exchanges
// from several servers, collector failures, link changes, late
// observations, long silences and restarts. After every batch it checks
// invariants on the database and on the correlator's own state. A failure
// prints the seed and the step; LT_CHAOS_SEED replays one seed, and
// LT_CHAOS_SEEDS and LT_CHAOS_STEPS make the run longer (make chaos).

func chaosEnv(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func TestChaosCorrelator(t *testing.T) {
	seeds, steps := chaosEnv("LT_CHAOS_SEEDS", 6), chaosEnv("LT_CHAOS_STEPS", 300)
	if testing.Short() {
		seeds, steps = 2, 150
	}
	first := uint64(chaosEnv("LT_CHAOS_SEED", 0))
	if first != 0 {
		seeds = 1
	} else {
		first = uint64(time.Now().UnixNano())
	}
	for i := range seeds {
		seed := first + uint64(i)
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			runChaos(t, seed, steps)
		})
	}
}

type chaos struct {
	t    *testing.T
	h    *harness
	r    *rand.Rand
	now  time.Duration
	step int
	seed uint64
	xids map[string]uint32
}

var (
	chaosMACs = []string{"00:1b:1b:00:00:01", "00:1b:1b:00:00:02", "00:1b:1b:00:00:03", "00:0e:8c:00:00:04",
		"02:42:ac:00:00:05", "00:15:5d:00:00:02", "00:15:5d:00:00:03"}
	chaosServers = map[string]string{"00:15:5d:00:00:02": "10.1.0.2", "00:15:5d:00:00:03": "10.1.0.3"}
)

func (c *chaos) mac() string { return chaosMACs[c.r.IntN(len(chaosMACs)-2)] } // clients only
func (c *chaos) ip() string  { return fmt.Sprintf("10.1.0.%d", 50+c.r.IntN(6)) }
func (c *chaos) iface() string {
	if c.r.IntN(10) == 0 {
		return "eth0"
	}
	return "eth1"
}

// at is the time of the next observation: usually now, sometimes late.
func (c *chaos) at() time.Time {
	if c.r.IntN(15) == 0 {
		return c.h.at(c.now - time.Duration(c.r.IntN(300))*time.Second)
	}
	return c.h.at(c.now)
}

func (c *chaos) send(o observation.Observation) {
	if o.Time.IsZero() {
		o.Time = c.at()
	}
	if o.Interface == "" {
		o.Interface = "eth1"
	}
	if o.Time.After(c.h.sim.Now()) {
		c.h.sim.Set(o.Time)
	}
	c.h.c.Handle(c.h.ctx, observation.Message{Observation: o})
}

func hw(s string) net.HardwareAddr {
	m, err := net.ParseMAC(s)
	if err != nil {
		panic(err)
	}
	return m
}

func (c *chaos) dhcpExchange() {
	client := c.mac()
	if c.xids == nil {
		c.xids = map[string]uint32{}
	}
	if c.r.IntN(3) == 0 {
		c.xids[client] = c.r.Uint32()
	}
	xid := c.xids[client]
	serverMAC := chaosMACs[len(chaosMACs)-1-c.r.IntN(2)]
	sid := netip.MustParseAddr(chaosServers[serverMAC])
	lease := uint32(600 + c.r.IntN(3600))
	yi := netip.MustParseAddr(c.ip())
	var m observation.DHCPMessage
	switch c.r.IntN(8) {
	case 0:
		m = observation.DHCPMessage{Op: 1, Type: observation.DHCPDiscover, XID: xid, CHAddr: client}
	case 1:
		m = observation.DHCPMessage{Op: 1, Type: observation.DHCPRequest, XID: xid, CHAddr: client, RequestedIP: yi, ServerID: sid}
	case 2:
		m = observation.DHCPMessage{Op: 2, Type: observation.DHCPOffer, XID: xid, CHAddr: client, YIAddr: yi, ServerID: sid, Lease: &lease}
	case 3, 4:
		m = observation.DHCPMessage{Op: 2, Type: observation.DHCPAck, XID: xid, CHAddr: client, YIAddr: yi, ServerID: sid, Lease: &lease,
			Routers: []netip.Addr{netip.MustParseAddr("10.1.0." + strconv.Itoa(1+c.r.IntN(2)))}, Present: []int{3, 51, 54}}
		if c.r.IntN(4) == 0 {
			m.GIAddr = netip.MustParseAddr("10.9.0.1")
		}
	case 5:
		m = observation.DHCPMessage{Op: 2, Type: observation.DHCPNak, XID: xid, CHAddr: client, ServerID: sid}
	case 6:
		m = observation.DHCPMessage{Op: 1, Type: observation.DHCPRelease, XID: xid, CHAddr: client, CIAddr: yi, ServerID: sid}
	default:
		m = observation.DHCPMessage{Op: 1, Type: observation.DHCPDecline, XID: xid, CHAddr: client, RequestedIP: yi}
	}
	o := observation.Observation{Source: observation.PassiveDHCP, MAC: hw(client), DHCP: &m}
	if m.Op == 2 {
		o.Source, o.MAC, o.IP = observation.PassiveDHCPServer, hw(serverMAC), sid
	}
	c.send(o)
}

func (c *chaos) do() {
	ip := netip.MustParseAddr(c.ip())
	switch n := c.r.IntN(100); {
	case n < 20:
		src := []observation.Source{observation.PassiveARP, observation.ARPScan, observation.PassiveIPv4}[c.r.IntN(3)]
		o := observation.Observation{Source: src, Interface: c.iface(), MAC: hw(c.mac()), IP: ip}
		if src == observation.PassiveARP {
			o.Meta = map[string]string{"arp": []string{"request", "reply", "gratuitous"}[c.r.IntN(3)]}
		}
		c.send(o)
	case n < 28:
		c.send(observation.Observation{Source: observation.KernelNeighbor, MAC: hw(c.mac()), IP: ip,
			NeighborState: []string{"REACHABLE", "STALE", "DELAY"}[c.r.IntN(3)]})
	case n < 40:
		states := []observation.ServiceState{observation.ServiceOpen, observation.ServiceRefused, observation.ServiceTimeout,
			observation.ServiceUnreachable, observation.ServiceLocalError}
		c.send(observation.Observation{Source: observation.TCPConnect, IP: ip,
			Service: &observation.ServiceResult{Proto: "tcp", Port: []int{80, 502}[c.r.IntN(2)], State: states[c.r.IntN(len(states))]}})
	case n < 44:
		c.send(observation.Observation{Source: observation.ICMPScan, IP: ip})
	case n < 52:
		ttl := uint32(c.r.IntN(3) * 60) // 0 is a goodbye
		mac := c.mac()
		c.send(observation.Observation{Source: observation.PassiveMDNS, MAC: hw(mac), IP: ip, Records: []observation.Record{
			{Name: fmt.Sprintf("dev%d.local", c.r.IntN(4)), Type: observation.RecordA, Addr: ip, TTL: &ttl, Flush: c.r.IntN(2) == 0}}})
	case n < 56:
		ttl := uint32(3600)
		c.send(observation.Observation{Source: observation.PassiveDNS, Records: []observation.Record{
			{Name: fmt.Sprintf("host%d.plant.example", c.r.IntN(4)), Type: observation.RecordPTR, Addr: ip, Value: "x", TTL: &ttl}}})
	case n < 58:
		c.send(observation.Observation{Source: observation.PassiveDHCP, MAC: hw(c.mac()), Hostname: fmt.Sprintf("HOST-%d", c.r.IntN(3)),
			NameType: observation.NameDHCP})
	case n < 78:
		c.dhcpExchange()
	case n < 81:
		state := []platform.State{platform.StateRunning, platform.StateFailed, platform.StateDisabled}[c.r.IntN(3)]
		c.h.collector(c.now, c.iface(), []string{platform.CollectorCapture, platform.CollectorNeighbor}[c.r.IntN(2)], state, "chaos")
	case n < 83:
		prefixes := [][]string{{"10.1.0.0/24"}, {"10.1.0.0/24", "10.2.0.0/24"}, nil}[c.r.IntN(3)]
		c.h.sim.Set(c.h.at(c.now))
		c.h.link("eth1", c.r.IntN(4) != 0, prefixes...)
	case n < 93:
		c.now += time.Duration(c.r.IntN(600)) * time.Second
	case n < 97:
		c.now += time.Duration(1+c.r.IntN(30)) * time.Hour // silence: presence, expiry, exchange idle
	default:
		c.restartCheck()
	}
}

func runChaos(t *testing.T, seed uint64, steps int) {
	c := &chaos{t: t, h: newHarness(t), r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), seed: seed}
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("seed %d step %d: panic: %v", seed, c.step, p)
		}
	}()
	for c.step = 0; c.step < steps; c.step++ {
		c.do()
		if c.step%25 == 24 {
			c.check()
		}
	}
	c.check()
	c.restartCheck()
	t.Logf("seed %d: %s; events by type: %s", seed, c.snapshot(),
		strings.Join(c.rows(`SELECT type || '=' || count(*) FROM events GROUP BY type ORDER BY type`), " "))
}

// restartCheck restarts the correlator and checks that the restart alone
// changes nothing that is recorded, and that the reloaded state matches.
func (c *chaos) restartCheck() {
	before := c.snapshot()
	c.h.restart()
	if after := c.snapshot(); after != before {
		c.fail("restart changed the database:\nbefore %s\nafter  %s", before, after)
	}
	c.checkState()
}

func (c *chaos) fail(format string, args ...any) {
	c.t.Helper()
	c.t.Fatalf("seed %d step %d: %s", c.seed, c.step, fmt.Sprintf(format, args...))
}

// snapshot summarises the tables a restart must leave alone.
func (c *chaos) snapshot() string {
	var parts []string
	for _, q := range []string{
		`SELECT count(*) || '/' || coalesce(max(id), 0) FROM events`,
		`SELECT count(*) || '/' || coalesce(sum(ended_at IS NULL), 0) FROM addresses`,
		`SELECT count(*) || '/' || coalesce(sum(ended_at IS NULL), 0) FROM name_records`,
		`SELECT count(*) || '/' || coalesce(sum(ended_at IS NULL), 0) FROM dhcp_leases`,
		`SELECT count(*) FROM endpoint_results`,
		`SELECT count(*) || '/' || group_concat(presence, '') FROM (SELECT presence FROM hosts ORDER BY host_id)`,
	} {
		parts = append(parts, strings.Join(c.h.query(q, scanText), ","))
	}
	return strings.Join(parts, " ")
}

func (c *chaos) rows(q string) []string { return c.h.query(q, scanText) }

func (c *chaos) check() {
	c.t.Helper()
	// Two open bindings of one address on one interface are a conflict,
	// flagged on both; a flagged binding has a rival.
	if bad := c.rows(`SELECT a.id || ' ' || a.ip FROM addresses a WHERE a.ended_at IS NULL AND
		(SELECT count(*) FROM addresses b WHERE b.ended_at IS NULL AND b.context_id = a.context_id AND b.ip = a.ip) > 1
		AND a.conflict = 0`); len(bad) > 0 {
		c.fail("open bindings sharing an address without the conflict flag: %v", bad)
	}
	if bad := c.rows(`SELECT a.id || ' ' || a.ip FROM addresses a WHERE a.ended_at IS NULL AND a.conflict = 1 AND
		(SELECT count(*) FROM addresses b WHERE b.ended_at IS NULL AND b.context_id = a.context_id AND b.ip = a.ip) < 2`); len(bad) > 0 {
		c.fail("conflict flag on a binding without a rival: %v", bad)
	}
	// Bindings belong to their host's context; intervals are ordered.
	if bad := c.rows(`SELECT a.id FROM addresses a JOIN hosts h ON h.host_id = a.host_id WHERE h.context_id != a.context_id
		OR a.last_seen < a.first_seen OR (a.ended_at IS NOT NULL AND a.ended_at < a.first_seen)`); len(bad) > 0 {
		c.fail("bindings out of shape: %v", bad)
	}
	// last_seen is the latest presence evidence: never before a kind's time.
	if bad := c.rows(`SELECT host_id FROM hosts WHERE last_seen < coalesce(last_frame_seen, 0)
		OR last_seen < coalesce(last_address_confirmed, 0) OR last_seen < coalesce(last_endpoint_answer, 0)`); len(bad) > 0 {
		c.fail("hosts with last_seen before an evidence time: %v", bad)
	}
	// One open lease per client; a lease's renewal never precedes it.
	if bad := c.rows(`SELECT chaddr FROM dhcp_leases WHERE ended_at IS NULL GROUP BY context_id, chaddr HAVING count(*) > 1`); len(bad) > 0 {
		c.fail("several open leases for one client: %v", bad)
	}
	// Endpoint history: the last result is the latest row's.
	if bad := c.rows(`SELECT e.id FROM endpoints e WHERE e.last_result != (SELECT r.result FROM endpoint_results r
		WHERE r.endpoint_id = e.id ORDER BY r.ts DESC, r.id DESC LIMIT 1)`); len(bad) > 0 {
		c.fail("endpoints whose last result is not their latest history row: %v", bad)
	}
	if bad := c.rows(`SELECT id FROM endpoints WHERE last_definite_result IS NOT NULL AND last_definite_at > last_result_at`); len(bad) > 0 {
		c.fail("definite answers after the last result: %v", bad)
	}
	// Name records linked by address belong to a host on the record's
	// interface.
	if bad := c.rows(`SELECT n.id FROM name_records n JOIN hosts h ON h.host_id = n.host_id WHERE h.context_id != n.context_id`); len(bad) > 0 {
		c.fail("name records linked to a host on another interface: %v", bad)
	}
	c.checkState()
}

// checkState compares the correlator's in-memory state with the database.
func (c *chaos) checkState() {
	c.t.Helper()
	var mem []string
	for k, hs := range c.h.c.st.holders {
		for _, b := range hs {
			mem = append(mem, fmt.Sprintf("%d %s %s %d", b.id, b.host.id, k.ip, boolInt(b.conflict)))
		}
	}
	sort.Strings(mem)
	db := c.h.query(`SELECT id, host_id, ip, conflict FROM addresses WHERE ended_at IS NULL`, func(r *sql.Rows) string {
		var id int64
		var host, ip string
		var conflict int
		if err := r.Scan(&id, &host, &ip, &conflict); err != nil {
			c.t.Fatal(err)
		}
		return fmt.Sprintf("%d %s %s %d", id, host, ip, conflict)
	})
	sort.Strings(db)
	if strings.Join(mem, "|") != strings.Join(db, "|") {
		c.fail("open bindings in memory and in the database differ:\nmemory   %v\ndatabase %v", mem, db)
	}
	var memLeases []string
	for k, l := range c.h.c.st.leases {
		memLeases = append(memLeases, fmt.Sprintf("%d %s %s", l.id, k.chaddr, l.ip))
	}
	sort.Strings(memLeases)
	dbLeases := c.rows(`SELECT id || ' ' || chaddr || ' ' || ip FROM dhcp_leases WHERE ended_at IS NULL`)
	sort.Strings(dbLeases)
	if strings.Join(memLeases, "|") != strings.Join(dbLeases, "|") {
		c.fail("open leases in memory and in the database differ:\nmemory   %v\ndatabase %v", memLeases, dbLeases)
	}
	var memRecords []string
	for _, r := range c.h.c.st.records {
		memRecords = append(memRecords, strconv.FormatInt(r.id, 10))
	}
	sort.Strings(memRecords)
	dbRecords := c.rows(`SELECT id FROM name_records WHERE ended_at IS NULL`)
	sort.Strings(dbRecords)
	if strings.Join(memRecords, "|") != strings.Join(dbRecords, "|") {
		c.fail("open name records in memory and in the database differ:\nmemory   %v\ndatabase %v", memRecords, dbRecords)
	}
}
