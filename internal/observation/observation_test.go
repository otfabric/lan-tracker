package observation

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestJSONRoundTrip(t *testing.T) {
	mac, _ := net.ParseMAC("00:1b:1b:aa:bb:01")
	in := Observation{
		Time: time.Date(2026, 10, 1, 12, 58, 0, 0, time.UTC), Source: KernelNeighbor, Interface: "eth1",
		MAC: mac, IP: netip.MustParseAddr("192.168.110.51"), NeighborState: "REACHABLE",
		Service: &ServiceResult{Proto: "tcp", Port: 502, State: ServiceOpen},
		Meta:    map[string]string{"k": "v"},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Observation
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	if out.MAC.String() != in.MAC.String() || out.IP != in.IP || !out.Time.Equal(in.Time) ||
		out.Service.Key() != "tcp/502" || out.NeighborState != "REACHABLE" || out.Meta["k"] != "v" {
		t.Errorf("round trip: %+v", out)
	}
}

func TestReadJSONL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int
		wantErr string
	}{
		{"valid with blank line", `{"time":"2026-10-01T10:00:00Z","source":"passive_arp","interface":"eth1","mac":"00:1b:1b:aa:bb:01","ip":"192.168.110.50"}

{"time":"2026-10-01T10:00:01Z","source":"tcp_connect","ip":"192.168.110.50","service":{"proto":"tcp","port":502,"state":"OPEN"}}`, 2, ""},
		{"unknown field", `{"time":"2026-10-01T10:00:00Z","source":"passive_arp","ip":"10.0.0.1","bogus":1}`, 0, "line 1: json: unknown field"},
		{"unknown source", `{"time":"2026-10-01T10:00:00Z","source":"telepathy","ip":"10.0.0.1"}`, 0, `unknown source "telepathy"`},
		{"bad mac", `{"time":"2026-10-01T10:00:00Z","source":"passive_arp","mac":"zz"}`, 0, "mac:"},
		{"empty observation", `{"time":"2026-10-01T10:00:00Z","source":"passive_arp"}`, 0, "neither mac, ip, hostname nor records"},
		{"missing time", `{"source":"passive_arp","ip":"10.0.0.1"}`, 0, "missing time"},
		{"error on line 2", "{\"time\":\"2026-10-01T10:00:00Z\",\"source\":\"passive_arp\",\"ip\":\"10.0.0.1\"}\n{", 1, "line 2:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := 0
			err := ReadJSONL(strings.NewReader(tt.input), func(Observation) error { n++; return nil })
			if n != tt.want {
				t.Errorf("read %d observations, want %d", n, tt.want)
			}
			if tt.wantErr == "" && err != nil || tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestBus(t *testing.T) {
	b := NewBus(2)
	o := Observation{Time: time.Unix(1, 0), Source: PassiveARP, IP: netip.MustParseAddr("10.0.0.1")}
	first, second := b.Publish(o), b.Publish(o)
	if !first || !second {
		t.Fatal("publish into a non-full bus failed")
	}
	if b.Publish(o) || b.Dropped() != 1 {
		t.Fatalf("full bus: dropped = %d, want 1", b.Dropped())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	processed := 0
	go func() {
		for m := range b.C() {
			if m.Barrier != nil {
				close(m.Barrier)
				continue
			}
			processed++
		}
	}()
	if err := b.PublishWait(ctx, o); err != nil {
		t.Fatal(err)
	}
	if err := b.Barrier(ctx); err != nil {
		t.Fatal(err)
	}
	if processed != 3 {
		t.Errorf("processed %d before the barrier, want 3", processed)
	}
}

func TestPublishWaitDoesNotDrop(t *testing.T) {
	b := NewBus(1)
	fill := Observation{Time: time.Unix(1, 0), Source: PassiveARP, IP: netip.MustParseAddr("10.0.0.1")}
	if !b.Publish(fill) {
		t.Fatal("fill")
	}
	id := Observation{Time: time.Unix(2, 0), Source: TCPConnect, IP: netip.MustParseAddr("10.0.0.2")}
	errc := make(chan error, 1)
	go func() {
		errc <- b.PublishWait(context.Background(), id)
	}()
	got := <-b.C()
	if got.Observation.Source != PassiveARP {
		t.Fatalf("first = %s", got.Observation.Source)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	got = <-b.C()
	if got.Observation.Source != TCPConnect {
		t.Fatalf("second = %s", got.Observation.Source)
	}
	if b.Dropped() != 0 {
		t.Errorf("Dropped = %d, want 0", b.Dropped())
	}
}

func TestBusCancellation(t *testing.T) {
	b := NewBus(0) // default size
	if cap(b.ch) != DefaultBusSize {
		t.Fatalf("default size = %d", cap(b.ch))
	}
	full := NewBus(1)
	o := Observation{Time: time.Unix(1, 0), Source: PassiveARP, IP: netip.MustParseAddr("10.0.0.1")}
	full.Publish(o)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := full.PublishWait(ctx, o); err == nil {
		t.Error("PublishWait on a full bus ignored cancellation")
	}
	if err := full.PublishLink(ctx, LinkState{Interface: "eth0"}); err == nil {
		t.Error("PublishLink on a full bus ignored cancellation")
	}
	if err := full.Barrier(ctx); err == nil {
		t.Error("Barrier on a full bus ignored cancellation")
	}
	// A barrier that is queued but never processed also honours ctx.
	empty := NewBus(1)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel2()
	if err := empty.Barrier(ctx2); err == nil {
		t.Error("Barrier returned without a consumer")
	}
}

func TestPublishOperatorWaitsForTheConsumer(t *testing.T) {
	b := NewBus(4)
	got := make(chan Operator, 1)
	go func() {
		for m := range b.C() {
			switch {
			case m.Operator != nil:
				got <- *m.Operator
			case m.Barrier != nil:
				close(m.Barrier)
			}
		}
	}()
	if err := b.PublishOperator(context.Background(), Operator{Kind: OpActiveDisabled, Actor: "bart"}); err != nil {
		t.Fatal(err)
	}
	select {
	case op := <-got:
		if op.Kind != OpActiveDisabled || op.Actor != "bart" {
			t.Errorf("operator = %+v", op)
		}
	default:
		t.Error("PublishOperator returned before the consumer saw the message")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	full := NewBus(1)
	full.ch <- Message{}
	if err := full.PublishOperator(ctx, Operator{}); !errors.Is(err, context.Canceled) {
		t.Errorf("PublishOperator on a full bus = %v", err)
	}
}

// The evidence kinds of docs/DATA_MODEL.md §2: a DHCP server's lease
// assertion and a cache entry never make a host present; an answered
// probe does, a timeout or a local failure does not.
func TestEvidence(t *testing.T) {
	ip := netip.MustParseAddr("10.0.0.1")
	mac := net.HardwareAddr{0, 1, 2, 3, 4, 5}
	svc := func(s ServiceState) *ServiceResult { return &ServiceResult{Proto: "tcp", Port: 502, State: s} }
	tests := []struct {
		o    Observation
		want Evidence
	}{
		{Observation{Source: PassiveARP, MAC: mac, IP: ip}, EvidenceAddress},
		{Observation{Source: ARPScan, MAC: mac, IP: ip}, EvidenceAddress},
		{Observation{Source: PassiveIPv4, MAC: mac, IP: ip}, EvidenceFrame},
		{Observation{Source: PassiveMDNS, MAC: mac, IP: ip}, EvidenceFrame},
		{Observation{Source: PassiveDNS, IP: ip, Hostname: "x"}, EvidenceNone},
		{Observation{Source: DNSQuery, Records: []Record{{Name: "x", Type: RecordA, Addr: ip}}}, EvidenceNone},
		{Observation{Source: MDNSQuery, MAC: mac, IP: ip}, EvidenceFrame},
		{Observation{Source: KernelNeighbor, MAC: mac, IP: ip, NeighborState: "REACHABLE"}, EvidenceAddress},
		{Observation{Source: KernelNeighbor, MAC: mac, IP: ip, NeighborState: "STALE"}, EvidenceCached},
		{Observation{Source: TCPConnect, IP: ip, Service: svc(ServiceOpen)}, EvidenceAnswer},
		{Observation{Source: TCPConnect, IP: ip, Service: svc(ServiceRefused)}, EvidenceAnswer},
		{Observation{Source: TCPConnect, IP: ip, Service: svc(ServiceTimeout)}, EvidenceNone},
		{Observation{Source: TCPConnect, IP: ip, Service: svc(ServiceUnreachable)}, EvidenceNone},
		{Observation{Source: TCPConnect, IP: ip, Service: svc(ServiceLocalError)}, EvidenceNone},
		{Observation{Source: ICMPScan, IP: ip}, EvidenceAnswer},
	}
	for _, tt := range tests {
		if got := tt.o.Evidence(); got != tt.want {
			t.Errorf("%s %s %v: Evidence = %d, want %d", tt.o.Source, tt.o.NeighborState, tt.o.Service, got, tt.want)
		}
	}
	if EvidenceCached.Present() || EvidenceNone.Present() || !EvidenceAnswer.Present() {
		t.Error("Present")
	}
}

// Records round-trip through JSONL; a missing TTL is unknown, not a
// goodbye.
func TestRecordsJSON(t *testing.T) {
	in := `{"time":"2026-10-01T10:00:00Z","source":"passive_mdns","mac":"00:01:02:03:04:05","ip":"10.0.0.1",` +
		`"records":[{"name":"plc.local","type":"A","addr":"10.0.0.9","ttl":120,"flush":true},{"name":"plc.local","type":"AAAA","addr":"fd00::9"}]}`
	var o Observation
	if err := json.Unmarshal([]byte(in), &o); err != nil {
		t.Fatal(err)
	}
	if len(o.Records) != 2 || *o.Records[0].TTL != 120 || !o.Records[0].Flush || o.Records[1].TTL != nil {
		t.Fatalf("records = %+v", o.Records)
	}
	out, _ := json.Marshal(o)
	var back Observation
	if err := json.Unmarshal(out, &back); err != nil || back.Records[0].Addr != o.Records[0].Addr {
		t.Errorf("round trip %s: %v", out, err)
	}
	for _, bad := range []string{
		`{"time":"2026-10-01T10:00:00Z","source":"passive_dns","records":[{"name":"x","type":"MX"}]}`,
		`{"time":"2026-10-01T10:00:00Z","source":"passive_dns","records":[{"type":"A"}]}`,
	} {
		if err := json.Unmarshal([]byte(bad), &o); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	only := `{"time":"2026-10-01T10:00:00Z","source":"dns_query","records":[{"name":"x.example","type":"A","addr":"10.0.0.2"}]}`
	if err := json.Unmarshal([]byte(only), &o); err != nil {
		t.Errorf("records alone: %v", err)
	}
}

func TestClosedBusFailsBlockingPublishes(t *testing.T) {
	b := NewBus(1)
	b.ch <- Message{} // full, and nobody reads
	b.Close()
	b.Close() // idempotent
	ctx := context.Background()
	for name, err := range map[string]error{
		"operator": b.PublishOperator(ctx, Operator{}),
		"barrier":  b.Barrier(ctx),
		"wait":     b.PublishWait(ctx, Observation{}),
		"link":     b.PublishLink(ctx, LinkState{}),
	} {
		if !errors.Is(err, ErrBusClosed) {
			t.Errorf("%s on a closed bus = %v", name, err)
		}
	}
	// A barrier already queued when the consumer stops.
	b2 := NewBus(4)
	errc := make(chan error, 1)
	go func() { errc <- b2.Barrier(ctx) }()
	<-b2.C() // taken but never closed
	b2.Close()
	if err := <-errc; !errors.Is(err, ErrBusClosed) {
		t.Errorf("pending barrier = %v", err)
	}
}
