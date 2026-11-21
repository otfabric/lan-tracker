package correlate

import (
	"context"
	"database/sql"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/observation"
)

// An ACK's configuration snapshot keeps every option it carried: names,
// both classless route options, the relay agent's sub-options and the
// options that did not decode. A stored snapshot that does not parse is
// read back as unknown after a restart, not as a reason to stop.
func TestLeaseConfigSnapshot(t *testing.T) {
	h := newHarness(t)
	route := []observation.DHCPRoute{{Destination: netip.MustParsePrefix("10.9.0.0/16"), Router: addr("10.1.0.254")}}
	ack := server(observation.DHCPAck, 1, plc, "10.1.0.2", "10.1.0.50", func(m *observation.DHCPMessage) {
		m.Lease, m.Domain, m.Search, m.Hostname = secs(3600), "plant.local", []string{"plant.local", "site.local"}, "plc-a"
		m.FQDN = &observation.DHCPFQDN{Flags: 1, Name: "plc-a.plant.local"}
		m.Routes, m.MSRoutes = route, route
		m.RelayAgent = &observation.DHCPRelay{CircuitID: "0001", RemoteID: "abcd"}
		m.Malformed = []int{33}
		m.Present = append(m.Present, 15, 119, 12, 81, 249, 82)
	})
	h.dhcp(0, dhcpA, "10.1.0.2", ack)
	leases := h.leases()
	if len(leases) != 1 {
		t.Fatalf("leases = %v", leases)
	}
	for _, want := range []string{`"domain":"plant.local"`, `"search":"plant.local,site.local"`, `"hostname":"plc-a"`,
		`"fqdn":"plc-a.plant.local"`, `"fqdn_flags":1`, `"classless_routes":"10.9.0.0/16 via 10.1.0.254"`,
		`"ms_classless_routes":"10.9.0.0/16 via 10.1.0.254"`, `"gateway_source":"option_121"`, `"relay_circuit":"0001"`,
		`"relay_remote":"abcd"`, `"malformed_options":[33]`} {
		if !strings.Contains(leases[0], want) {
			t.Errorf("lease config lacks %s:\n%s", want, leases[0])
		}
	}

	h.flush()
	if err := h.st.Submit(h.ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE dhcp_leases SET config_json = 'not json'`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE dhcp_servers SET config_json = 'not json'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	h.restart()
	// The same ACK after the restart extends the open lease.
	h.dhcp(time.Minute, dhcpA, "10.1.0.2", ack)
	if leases := h.leases(); len(leases) != 1 || !strings.Contains(leases[0], "acks=2") {
		t.Errorf("after the restart: %v", leases)
	}
}
