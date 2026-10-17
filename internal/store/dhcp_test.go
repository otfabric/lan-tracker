package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/store"
)

func TestDHCPServers(t *testing.T) {
	st, _, _ := seed(t)
	r := reader(t, st)
	ctx := context.Background()
	if got, err := r.DHCPServers(ctx, store.DHCPServerFilter{}); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no servers = %#v, %v", got, err)
	}
	ms := t0.UnixMilli()
	for _, row := range []struct {
		id                                int
		serverID, relay, mac, ip, st, cfg string
		last                              int64
	}{
		{1, "", "", "02:00:00:00:00:66", "10.1.0.66", "unexpected", `{}`, ms},
		{2, "10.1.0.2", "", "00:15:5d:00:00:02", "10.1.0.2", "allowed", `{"router":"10.1.0.1","dns":"10.1.0.2"}`, ms + 1000},
		{3, "10.1.0.2", "", "00:15:5d:00:00:03", "10.1.0.2", "allowed", `not json`, ms + 2000},
		{4, "10.1.0.2", "10.1.0.1", "00:00:5e:00:01:01", "", "allowed", `{}`, ms - int64(48*time.Hour/time.Millisecond)},
	} {
		exec(t, st, `INSERT INTO dhcp_servers (id, context_id, server_id, relay, mac, ip, status, config_json, first_seen, last_seen)
			VALUES (?, (SELECT id FROM network_contexts WHERE interface = 'eth1'), ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.id, row.serverID, row.relay, row.mac, row.ip, row.st, row.cfg, min(ms, row.last), row.last)
	}
	got, err := r.DHCPServers(ctx, store.DHCPServerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, d := range got {
		order = append(order, fmt.Sprintf("%s|%s|%s|%s|%s", d.ServerID, d.Relay, d.MAC, d.Status, d.Config["router"]))
		if d.Interface != "eth1" || d.Config == nil {
			t.Errorf("server %+v", d)
		}
	}
	want := []string{
		"10.1.0.2||00:15:5d:00:00:03|allowed|", // the latest sender of an identity first; a bad config reads as none
		"10.1.0.2||00:15:5d:00:00:02|allowed|10.1.0.1",
		"10.1.0.2|10.1.0.1|00:00:5e:00:01:01|allowed|",
		"||02:00:00:00:00:66|unexpected|", // unknown identities last
	}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("servers:\n got %v\nwant %v", order, want)
	}
	if !got[1].LastSeen.Equal(t0.Add(time.Second)) || got[1].FirstSeen.IsZero() {
		t.Errorf("times = %v..%v", got[1].FirstSeen, got[1].LastSeen)
	}
	if got, _ := r.DHCPServers(ctx, store.DHCPServerFilter{Status: "UNEXPECTED"}); len(got) != 1 || got[0].MAC != "02:00:00:00:00:66" {
		t.Errorf("status filter = %+v", got)
	}
	if got, _ := r.DHCPServers(ctx, store.DHCPServerFilter{Interface: "eth0"}); len(got) != 0 {
		t.Errorf("interface filter = %+v", got)
	}

	counts, err := r.DHCPServerCounts(ctx, t0.Add(-24*time.Hour))
	if err != nil || counts["eth1"]["allowed"] != 2 || counts["eth1"]["unexpected"] != 1 || len(counts) != 1 {
		t.Errorf("counts = %v, %v (the relayed row is older than the window)", counts, err)
	}
}
