package config

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// LAN Sentinel's own reference file converts to a valid LAN Tracker file
// with probing off, identification and UDP probes gone, packet budgets
// replaced by the start-rate defaults and the paths renamed.
func TestConvertLegacy(t *testing.T) {
	data, err := os.ReadFile("testdata/lan-sentinel-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out, r, err := Convert(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Errors) > 0 {
		t.Fatalf("converted file does not validate: %v\n%s", r.Errors, out)
	}
	l, err := load(out, "converted", LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := l.Config
	for _, ic := range c.Interfaces {
		if ic.Active.Enabled {
			t.Errorf("%s: active discovery enabled after conversion", ic.Name)
		}
	}
	if c.Interfaces[1].Active.Networks[0].String() != "192.168.110.0/24" {
		t.Errorf("networks lost: %v", c.Interfaces[1].Active.Networks)
	}
	if c.Storage.Path != "/data/lan-tracker/hosts.db" || c.API.Socket != "/run/lan-tracker/api.sock" {
		t.Errorf("paths = %s, %s", c.Storage.Path, c.API.Socket)
	}
	d := Defaults().Active
	if c.Active.MaxStartsPerSecond != d.MaxStartsPerSecond || c.Active.Budgets != d.Budgets || c.Active.MinTargetInterval != d.MinTargetInterval {
		t.Errorf("rates not reset to the defaults: %+v", c.Active)
	}
	if c.Active.TCP.Targets[0].Port != 502 || c.Active.ARP.Interval.String() != "5m" {
		t.Errorf("operator choices lost: %+v", c.Active)
	}
	for _, gone := range []string{"identify", "udp", "address_overlap", "packets_per_second", "max_concurrent_probes", "lan-sentinel"} {
		if strings.Contains(string(out), gone) {
			t.Errorf("output still mentions %q:\n%s", gone, out)
		}
	}
	keys := func(notes []ConvertNote) []string {
		var out []string
		for _, n := range notes {
			out = append(out, n.Key)
		}
		return out
	}
	for _, k := range []string{"interfaces[1].active.identify", "active.udp", "active.identify", "profiles.identify.udp", "profiles.identify", "identity.address_overlap"} {
		if !slices.Contains(keys(r.Removed), k) {
			t.Errorf("removed %v lacks %s", keys(r.Removed), k)
		}
	}
	for _, k := range []string{"version", "interfaces[1].active.enabled", "active.max_packets_per_second", "active.budgets", "storage.path"} {
		if !slices.Contains(keys(r.Changed), k) {
			t.Errorf("changed %v lacks %s", keys(r.Changed), k)
		}
	}
	if !strings.Contains(strings.Join(r.Attention, "\n"), "active discovery is off on eth1") {
		t.Errorf("attention = %v", r.Attention)
	}
}

func TestConvertRefuses(t *testing.T) {
	for _, in := range []string{"version: 2\ninterfaces: [{name: eth0}]\n", "- a\n", "interfaces: []\n", ""} {
		if _, _, err := Convert([]byte(in)); !errors.Is(err, ErrNotLegacy) {
			t.Errorf("%q: %v", in, err)
		}
	}
	if _, _, err := Convert([]byte("version: [")); err == nil {
		t.Error("bad YAML accepted")
	}
	// A file that still does not validate is converted, with its errors.
	out, r, err := Convert([]byte("version: 1\ninterfaces: [{name: eth0, active: {enabled: true, networks: [10.0.0.0/8]}}]\n" +
		"active: {min_target_interval: 10s, tcp: {enabled: true}}\n"))
	if err != nil || len(r.Errors) == 0 || out == nil {
		t.Errorf("want validation errors, got %v %v", r.Errors, err)
	}
	if !strings.Contains(string(out), "min_target_interval: 10s") || !strings.Contains(strings.Join(r.Attention, " "), "active.tcp.targets is unset") {
		t.Errorf("out = %s, attention = %v", out, r.Attention)
	}
}
