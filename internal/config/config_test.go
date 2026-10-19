package config

import (
	"errors"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// minimal is the smallest valid live configuration.
const minimal = "version: 1\ninterfaces:\n  - name: eth0\n"

func mustLoad(t *testing.T, yml string, o LoadOptions) *Loaded {
	t.Helper()
	l, err := load([]byte(yml), "test.yaml", o)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return l
}

func validationErrors(t *testing.T, yml string) []FieldError {
	t.Helper()
	_, err := load([]byte(yml), "test.yaml", LoadOptions{})
	if err == nil {
		return nil
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("load: unexpected error type %T: %v", err, err)
	}
	return ve.Errors
}

// Another schema version is refused; keys outside the schema, such as
// identification or UDP probe settings, are unknown keys.
func TestSchemaVersionAndUnknownKeys(t *testing.T) {
	errs := validationErrors(t, "version: 2\ninterfaces:\n  - name: eth0\n")
	if len(errs) != 1 || errs[0].Key != "version" || !strings.Contains(errs[0].Msg, "must be 1") {
		t.Errorf("version 2: %v", errs)
	}
	for _, unknown := range []string{
		"active: { udp: { enabled: false } }\n",
		"active: { identify: { interval: 1h } }\n",
		"active: { max_packets_per_second: 20 }\n",
		"identity: { identifiers: { mdns: true } }\n",
		"profiles: { x: { udp: [enip] } }\n",
	} {
		if errs := validationErrors(t, minimal+unknown); len(errs) == 0 || !strings.Contains(errs[0].Msg, "unknown key") {
			t.Errorf("%q: %v", unknown, errs)
		}
	}
	if errs := validationErrors(t, minimal+"    active: { identify: [{name: modbus}] }\n"); len(errs) == 0 {
		t.Error("interfaces[].active.identify accepted")
	}
}

func TestDocExamplesLoad(t *testing.T) {
	for _, file := range []string{"testdata/architecture-example.yaml", "testdata/replay-example.yaml"} {
		t.Run(file, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if errs := validationErrors(t, string(data)); len(errs) > 0 {
				t.Errorf("invalid: %v", errs)
			}
		})
	}
}

func TestDefaultsApplied(t *testing.T) {
	l := mustLoad(t, minimal, LoadOptions{})
	c := l.Config
	if c.Storage.Path != DefaultDBPath || c.API.Socket != DefaultSocketPath || c.Logging.Format != "journald" {
		t.Errorf("defaults not applied: %+v %+v %+v", c.Storage, c.API, c.Logging)
	}
	if c.Neighbor.ResyncInterval.D() != 10*time.Minute {
		t.Errorf("resync = %s", c.Neighbor.ResyncInterval)
	}
	if !c.Interfaces[0].PassiveEnabled() || c.Interfaces[0].Active.Enabled {
		t.Error("live interface should default to passive on, active off")
	}
	if c.Active.MaxStartsPerSecond != 5 || c.Active.Budgets.TCP.ConnectsPerSecond != 1 || c.Active.Budgets.TCP.MaxConcurrentPerHost != 1 ||
		c.Active.MaxConcurrentJobs != 4 || c.Active.MaxSweepTargets != 1024 || len(c.Active.TCP.Targets) != 4 || c.Active.TCP.Enabled {
		t.Errorf("safety defaults wrong: %+v", c.Active)
	}
}

func TestUnknownKeysReportedWithLines(t *testing.T) {
	yml := minimal + "active:\n  max_packets_per_secnd: 5\n  budgets:\n    tcp: { conects_per_second: 1 }\nbogus: true\n"
	errs := validationErrors(t, yml)
	want := []FieldError{
		{Key: "active.max_packets_per_secnd", Line: 5, Msg: "unknown key"},
		{Key: "active.budgets.tcp.conects_per_second", Line: 7, Msg: "unknown key"},
		{Key: "bogus", Line: 8, Msg: "unknown key"},
	}
	if len(errs) != len(want) {
		t.Fatalf("got %v, want %v", errs, want)
	}
	for i := range want {
		if errs[i] != want[i] {
			t.Errorf("error %d = %+v, want %+v", i, errs[i], want[i])
		}
	}
}

func TestPrecedenceAndSources(t *testing.T) {
	yml := minimal + "storage: { path: /tmp/file.db }\nlogging: { level: debug }\n"
	l := mustLoad(t, yml, LoadOptions{
		Environ: []string{
			"LAN_TRACKER_LOGGING_LEVEL=warn",
			"LAN_TRACKER_ACTIVE_MAX_STARTS_PER_SECOND=6",
			"LAN_TRACKER_IDENTITY_HOSTNAME_PREFERENCE=dhcp, mdns",
			"LAN_TRACKER_ACTIVE_DISABLED=1", // reserved, not a key
			"LAN_TRACKER_CONFIG=/x.yaml",    // reserved, not a key
			"PATH=/bin",
		},
		Flags: []Override{{Key: "storage.path", Value: "/tmp/flag.db", Origin: "--db"}},
	})
	c := l.Config
	tests := []struct {
		key, source string
		ok          bool
	}{
		{"storage.path", "flag --db", c.Storage.Path == "/tmp/flag.db"},
		{"logging.level", "env LAN_TRACKER_LOGGING_LEVEL", c.Logging.Level == "warn"},
		{"active.max_starts_per_second", "env LAN_TRACKER_ACTIVE_MAX_STARTS_PER_SECOND", c.Active.MaxStartsPerSecond == 6},
		{"identity.hostname_preference", "env LAN_TRACKER_IDENTITY_HOSTNAME_PREFERENCE", strings.Join(c.Identity.HostnamePreference, ",") == "dhcp,mdns"},
		{"interfaces[0].name", "file test.yaml", c.Interfaces[0].Name == "eth0"},
		{"presence.stale", "default", c.Presence.Stale.D() == 24*time.Hour},
	}
	for _, tt := range tests {
		if !tt.ok {
			t.Errorf("%s: wrong value", tt.key)
		}
		if got := l.SourceOf(tt.key); got != tt.source {
			t.Errorf("%s: source %q, want %q", tt.key, got, tt.source)
		}
	}
}

func TestEnvErrors(t *testing.T) {
	_, err := load([]byte(minimal), "test.yaml", LoadOptions{Environ: []string{
		"LAN_TRACKER_LOGING_LEVEL=info",
		"LAN_TRACKER_ACTIVE_JITTER=lots",
	}})
	var ve *ValidationError
	if !errors.As(err, &ve) || len(ve.Errors) != 2 {
		t.Fatalf("want 2 validation errors, got %v", err)
	}
	if ve.Errors[0].Key != "LAN_TRACKER_LOGING_LEVEL" || !strings.Contains(ve.Errors[1].Msg, "invalid number") {
		t.Errorf("unexpected errors: %v", ve.Errors)
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name    string
		yml     string
		wantKey string // "" means valid
		wantMsg string
	}{
		{"minimal valid", minimal, "", ""},
		{"no interfaces", "version: 1\n", "interfaces", "at least one"},
		{"wrong version", "version: 3\ninterfaces: [{name: eth0}]\n", "version", "must be 1"},
		{"duplicate interface", "version: 1\ninterfaces: [{name: eth0}, {name: eth0}]\n", "interfaces[1].name", "more than once"},
		{"long interface name", "version: 1\ninterfaces: [{name: averyveryverylongname}]\n", "interfaces[0].name", "15 characters"},
		{"active needs networks", minimal + "    active: { enabled: true }\n", "interfaces[0].active.networks", "at least one network"},
		{"wide scan refused", minimal + "    active: { enabled: true, networks: [10.0.0.0/16] }\n", "interfaces[0].active.networks[0]", "wider than /24"},
		{"wide scan allowed", minimal + "    active: { enabled: true, networks: [10.0.0.0/23] }\nactive: { allow_wide_scan: true }\n", "", ""},
		{"sweep beyond the default target count", minimal + "    active: { enabled: true, networks: [10.0.0.0/21] }\nactive: { allow_wide_scan: true }\n",
			"interfaces[0].active.networks", "hold 2046 addresses to sweep, more than active.max_sweep_targets (1024)"},
		{"networks adding up beyond it", minimal + "    active: { enabled: true, networks: [10.0.0.0/22, 10.1.0.0/24] }\nactive: { allow_wide_scan: true }\n",
			"interfaces[0].active.networks", "hold 1276 addresses"},
		{"excludes count", minimal + "    active: { enabled: true, networks: [10.0.0.0/22, 10.1.0.0/24], exclude: [10.1.0.0/24] }\nactive: { allow_wide_scan: true }\n", "", ""},
		{"expert override", minimal + "    active: { enabled: true, networks: [10.0.0.0/14] }\nactive: { allow_wide_scan: true, max_sweep_targets: 262144 }\n", "", ""},
		{"override needs allow_wide_scan", minimal + "    active: { enabled: true, networks: [10.0.0.0/24] }\nactive: { max_sweep_targets: 100000 }\n",
			"active.max_sweep_targets", "needs active.allow_wide_scan: true"},
		{"override keeps the default rates", minimal + "    active: { enabled: true, networks: [10.0.0.0/24] }\nactive: { allow_wide_scan: true, max_sweep_targets: 100000, budgets: { arp: { starts_per_second: 15 } } }\n",
			"active.budgets.arp.starts_per_second", "exceeds its default 5"},
		{"override keeps the default concurrency", minimal + "    active: { enabled: true, networks: [10.0.0.0/24] }\nactive: { allow_wide_scan: true, max_sweep_targets: 100000, max_concurrent_jobs: 20 }\n",
			"active.max_concurrent_jobs", "exceeds its default 4"},
		{"target count range", minimal + "    active: { enabled: true, networks: [10.0.0.0/24] }\nactive: { max_sweep_targets: 0 }\n",
			"active.max_sweep_targets", "must be between 1 and 16777216"},
		{"ipv6 scan refused", minimal + "    active: { enabled: true, networks: [\"fd00::/120\"] }\n", "interfaces[0].active.networks[0]", "only IPv4"},
		{"host bits", minimal + "    active: { enabled: true, networks: [192.168.1.5/24] }\n", "interfaces[0].active.networks[0]", "host bits"},
		{"exclude ip and cidr", minimal + "    active: { enabled: true, networks: [192.168.1.0/24], exclude: [192.168.1.1, 192.168.1.248/29] }\n", "", ""},
		{"prefixes on live interface", minimal + "    prefixes: [192.168.1.0/24]\n", "interfaces[0].prefixes", "only allowed on replay"},
		{"promisc without passive", minimal + "    passive: { enabled: false, promiscuous: true }\n", "interfaces[0].passive.promiscuous", "requires passive"},
		{"replay needs prefixes", "version: 1\ninterfaces:\n  - name: eth1\n    replay: { file: x.jsonl }\n", "interfaces[0].prefixes", "required"},
		{"replay bad extension", "version: 1\ninterfaces:\n  - name: eth1\n    prefixes: [10.0.0.0/24]\n    replay: { file: x.txt }\n", "interfaces[0].replay.file", "must end in"},
		{"replay gzipped capture", "version: 1\ninterfaces:\n  - name: eth1\n    prefixes: [10.0.0.0/24]\n    replay: { file: site.pcapng.gz }\n", "", ""},
		{"replay and live mixed", "version: 1\ninterfaces:\n  - name: eth0\n  - name: eth1\n    prefixes: [10.0.0.0/24]\n    replay: { file: x.jsonl }\n", "interfaces", "cannot be mixed"},
		{"replay exclusive with active", "version: 1\ninterfaces:\n  - name: eth1\n    prefixes: [10.0.0.0/24]\n    replay: { file: x.jsonl }\n    active: { enabled: true, networks: [10.0.0.0/24] }\n", "interfaces[0].active.enabled", "mutually exclusive"},
		{"budget above global", minimal + "active: { budgets: { arp: { starts_per_second: 30 } } }\n", "active.budgets.arp.starts_per_second", "exceeds the global"},
		{"tcp connects above global", minimal + "active: { budgets: { tcp: { connects_per_second: 7 } } }\n", "active.budgets.tcp.connects_per_second", "exceeds the global"},
		{"tcp per host above per interface", minimal + "active: { budgets: { tcp: { max_concurrent_per_host: 5 } } }\n", "active.budgets.tcp.max_concurrent_per_interface", "at least max_concurrent_per_host"},
		{"tcp enabled without targets", minimal + "active: { tcp: { enabled: true, targets: [] } }\n", "active.tcp.targets", "at least one target"},
		{"tcp duplicate port", minimal + "active: { tcp: { targets: [{port: 502, timeout: 1s}, {port: 502, timeout: 1s}] } }\n", "active.tcp.targets[1].port", "more than once"},
		{"jitter too high", minimal + "active: { jitter: 0.9 }\n", "active.jitter", "between 0 and 0.5"},
		{"presence order", minimal + "presence: { active: 1h, recent: 30m }\n", "presence.recent", "longer than presence.active"},
		{"bad name type", minimal + "identity: { hostname_preference: [mdns, wins] }\n", "identity.hostname_preference[1]", "must be one of"},
		{"profile without probes", minimal + "profiles: { empty: {} }\n", "profiles.empty", "enables no probe"},
		{"listen not loopback", minimal + "api: { listen: \"0.0.0.0:9734\" }\n", "api.listen", "loopback"},
		{"listen loopback", minimal + "api: { listen: \"127.0.0.1:9734\" }\n", "", ""},
		{"socket path too long", minimal + "api: { socket: /" + strings.Repeat("x", 110) + " }\n", "api.socket", "limited to 107"},
		{"bad log level", minimal + "logging: { level: verbose }\n", "logging.level", "must be one of"},
		{"db size too small", minimal + "storage: { retention: { max_db_size: 1MB } }\n", "storage.retention.max_db_size", "at least 10MB"},
		{"ring too small", minimal + "passive: { ring_size: 64KiB }\n", "passive.ring_size", "between 256KiB and 256MiB, got 64KiB"},
		{"ring too large", minimal + "passive: { ring_size: 1GB }\n", "passive.ring_size", "between 256KiB and 256MiB"},
		{"ring size", minimal + "passive: { ring_size: 8MiB }\n", "", ""},
		{"names", minimal + "active: { names: { enabled: true, service_types: [_http._tcp, _modbus._tcp], resolvers: [192.168.0.2] } }\n", "", ""},
		{"names bad service type", minimal + "active: { names: { service_types: [http] } }\n", "active.names.service_types[0]", "DNS-SD service type"},
		{"names duplicate service type", minimal + "active: { names: { service_types: [_http._tcp, _http._tcp] } }\n", "active.names.service_types[1]", "more than once"},
		{"names bad resolver", minimal + "active: { names: { resolvers: [0.0.0.0] } }\n", "active.names.resolvers[0]", "not a unicast address"},
		{"names asking nothing", minimal + "active: { names: { enabled: true, mdns: false } }\n", "active.names", "nothing would be asked"},
		{"names query cap", minimal + "active: { names: { max_queries_per_pass: 0 } }\n", "active.names.max_queries_per_pass", "between 1 and 1024"},
		{"names budget above global", minimal + "active: { budgets: { names: { starts_per_second: 9 } } }\n", "active.budgets.names.starts_per_second", "exceeds the global"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validationErrors(t, tt.yml)
			if tt.wantKey == "" {
				if len(errs) > 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}
				return
			}
			for _, fe := range errs {
				if fe.Key == tt.wantKey && strings.Contains(fe.Msg, tt.wantMsg) {
					return
				}
			}
			t.Errorf("want error on %s containing %q, got %v", tt.wantKey, tt.wantMsg, errs)
		})
	}
}

func TestErrorLinesFromFile(t *testing.T) {
	yml := minimal + "presence:\n  active: 1h\n  recent: 30m\n"
	errs := validationErrors(t, yml)
	if len(errs) != 1 || errs[0].Line != 6 {
		t.Fatalf("want one error on line 6, got %v", errs)
	}
}

func TestScalarTypes(t *testing.T) {
	tests := []struct {
		in   string
		want ByteSize
		bad  bool
	}{
		{"200MB", 200_000_000, false},
		{"1GiB", 1 << 30, false},
		{"512", 512, false},
		{"10 KB", 10_000, false},
		{"-1MB", 0, true},
		{"lots", 0, true},
	}
	for _, tt := range tests {
		var b ByteSize
		err := b.parse(tt.in)
		if (err != nil) != tt.bad || (!tt.bad && b != tt.want) {
			t.Errorf("parse(%q) = %d, %v", tt.in, b, err)
		}
	}
	if s := ByteSize(2 << 20).String(); s != "2MiB" {
		t.Errorf("2 MiB prints as %s", s)
	}
	if s := ByteSize(200_000_000).String(); s != "200MB" {
		t.Errorf("String = %q", s)
	}
	for in, want := range map[time.Duration]string{
		5 * time.Minute: "5m", 168 * time.Hour: "168h", 750 * time.Millisecond: "750ms", 90 * time.Second: "90s",
	} {
		if got := Duration(in).String(); got != want {
			t.Errorf("Duration(%v) = %q, want %q", in, got, want)
		}
	}
	var a AddrOrPrefix
	if err := a.UnmarshalText([]byte("192.168.1.1")); err != nil || !a.IsSingleIP() {
		t.Errorf("single IP: %v %v", a, err)
	}
	if b, _ := a.MarshalText(); string(b) != "192.168.1.1" {
		t.Errorf("MarshalText = %s", b)
	}
	if err := a.UnmarshalText([]byte("nope")); err == nil {
		t.Error("expected error for invalid address")
	}
}

// No dhcp.servers is no allowlist; an empty list expects no server.
func TestDHCPAllowlist(t *testing.T) {
	base := "version: 1\ninterfaces:\n  - name: eth0\n"
	for _, tt := range []struct {
		name, yml string
		checked   bool
		allowed   string
	}{
		{"no allowlist", base, false, ""},
		{"none expected", base + "    dhcp: { servers: [] }\n", true, ""},
		{"one server", base + "    dhcp: { servers: [192.168.0.1] }\n", true, "192.168.0.1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := mustLoad(t, tt.yml, LoadOptions{}).Config.Interfaces[0].DHCP
			if d.Checked() != tt.checked {
				t.Errorf("Checked() = %v", d.Checked())
			}
			if d.Allowed(netip.Addr{}) || d.Allowed(netip.MustParseAddr("192.168.0.9")) {
				t.Error("an unknown or unlisted identity is allowed")
			}
			if tt.allowed != "" && !d.Allowed(netip.MustParseAddr(tt.allowed)) {
				t.Errorf("%s not allowed", tt.allowed)
			}
			s := Summarize(mustLoad(t, tt.yml, LoadOptions{}).Config).Interfaces[0].DHCPServers
			if (s != nil) != tt.checked || s != nil && strings.Join(*s, ",") != tt.allowed {
				t.Errorf("summary = %v", s)
			}
		})
	}
}

func TestActiveText(t *testing.T) {
	base := "version: 1\ninterfaces:\n  - name: eth0\n"
	on := "    active: { enabled: true, networks: [192.168.0.0/24], exclude: [192.168.0.1] }\n"
	for _, tt := range []struct{ name, yml, want string }{
		{"off", base, "off"},
		{"arp", base + on, "arp every 15m on eth0 (192.168.0.0/24 exclude 192.168.0.1)"},
		{"no probe", base + on + "active: { arp: { enabled: false } }\n", "no probe enabled on eth0 (192.168.0.0/24 exclude 192.168.0.1)"},
		{"several", base + on + "  - name: eth1\n    active: { enabled: true, networks: [10.0.0.0/24] }\n  - name: eth2\n" +
			"active: { icmp: { enabled: true }, tcp: { enabled: true, targets: [{port: 502, name: modbus, timeout: 1s}] } }\n",
			"arp every 15m, icmp every 15m, tcp 502 (modbus) every 15m on eth0 (192.168.0.0/24 exclude 192.168.0.1), eth1 (10.0.0.0/24)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := Summarize(mustLoad(t, tt.yml, LoadOptions{}).Config).ActiveText(); got != tt.want {
				t.Errorf("ActiveText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDiff(t *testing.T) {
	old := Defaults()
	old.Interfaces = []InterfaceConfig{{Name: "eth1"}}
	if d, err := Diff(old, old); err != nil || d == nil || len(d) != 0 {
		t.Errorf("no change = %#v, %v", d, err)
	}
	next := *old
	next.Interfaces = []InterfaceConfig{
		{Name: "eth1", Active: InterfaceActive{Enabled: true, Networks: []netip.Prefix{netip.MustParsePrefix("192.168.110.0/24")}}},
		{Name: "eth2"},
	}
	next.Logging.Level = "debug"
	d, err := Diff(old, &next)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Change{}
	for _, c := range d {
		got[c.Key] = c
	}
	for _, want := range []Change{
		{"interfaces[0].active.enabled", "false", "true"},
		{"interfaces[0].active.networks", "", "[192.168.110.0/24]"},
		{"interfaces[1].name", "", "eth2"},
		{"logging.level", "info", "debug"},
	} {
		if got[want.Key] != want {
			t.Errorf("%s = %+v, want %+v", want.Key, got[want.Key], want)
		}
	}
	if _, ok := got["interfaces[0].name"]; ok || !slices.IsSortedFunc(d, func(a, b Change) int { return strings.Compare(a.Key, b.Key) }) {
		t.Errorf("diff = %+v", d)
	}
	// The other way round, keys only the old side has have an empty new value.
	back, _ := Diff(&next, old)
	if !slices.Contains(back, Change{"interfaces[1].name", "eth2", ""}) {
		t.Errorf("removed interface = %+v", back)
	}
}

func TestSettingsAndSummary(t *testing.T) {
	data, err := os.ReadFile("testdata/architecture-example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	l := mustLoad(t, string(data), LoadOptions{Environ: []string{"LAN_TRACKER_ACTIVE_JITTER=0.2"}})
	settings, err := l.Settings()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Setting{}
	for _, s := range settings {
		got[s.Key] = s
	}
	checks := []Setting{
		{"active.jitter", "0.2", "env LAN_TRACKER_ACTIVE_JITTER"},
		{"interfaces[1].active.networks", "[192.168.110.0/24, 192.168.120.0/24]", "file test.yaml"},
		{"interfaces[1].active.exclude", "[192.168.110.1]", "file test.yaml"},
		{"storage.retention.max_db_size", "200MB", "file test.yaml"},
		{"active.budgets.tcp.max_concurrent_per_host", "1", "file test.yaml"},
		{"profiles.modbus.tcp", "[502]", "file test.yaml"},
	}
	for _, want := range checks {
		if got[want.Key] != want {
			t.Errorf("%s = %+v, want %+v", want.Key, got[want.Key], want)
		}
	}

	s := Summarize(l.Config)
	// arp 5 + tcp 1 = 6, capped at the global 5.
	if s.MaxStartsPerSecond != 5 {
		t.Errorf("MaxStartsPerSecond = %v, want 5", s.MaxStartsPerSecond)
	}
	if len(s.Interfaces) != 2 || !s.Interfaces[1].Active || s.Interfaces[0].Active {
		t.Errorf("interfaces = %+v", s.Interfaces)
	}
	if len(s.Probes) != 3 || s.Probes[1] != "tcp 80 (http), 443 (https), 8080 (http-alt), 502 (modbus) every 15m" ||
		s.Probes[2] != "names (mdns, dns via 1 resolver) every 30m" {
		t.Errorf("probes = %v", s.Probes)
	}
}

func TestResolvePath(t *testing.T) {
	tests := []struct {
		explicit string
		env      []string
		want     string
	}{
		{"/a.yaml", []string{"LAN_TRACKER_CONFIG=/b.yaml"}, "/a.yaml"},
		{"", []string{"LAN_TRACKER_CONFIG=/b.yaml"}, "/b.yaml"},
		{"", nil, DefaultConfigPath},
	}
	for _, tt := range tests {
		if got := ResolvePath(tt.explicit, tt.env); got != tt.want {
			t.Errorf("ResolvePath(%q, %v) = %q, want %q", tt.explicit, tt.env, got, tt.want)
		}
	}
}

// The reference configs in deploy/ must stay valid.
func TestDeployConfigs(t *testing.T) {
	for _, file := range []string{"../../deploy/config.yaml", "../../deploy/config.dev.yaml", "../../deploy/config.active-example.yaml", "../../deploy/config.all.yaml"} {
		t.Run(file, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if errs := validationErrors(t, string(data)); len(errs) > 0 {
				t.Errorf("invalid: %v", errs)
			}
		})
	}
}
