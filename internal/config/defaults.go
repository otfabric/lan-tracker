package config

import "time"

// Compiled default paths (docs/ARCHITECTURE.md §6). LAN Tracker runs on
// Linux only.
const (
	DefaultConfigPath = "/etc/lan-tracker/config.yaml"
	DefaultDBPath     = "/data/lan-tracker/hosts.db"
	DefaultSocketPath = "/run/lan-tracker/api.sock"
	// maxSocketPath is the longest Unix socket path Linux accepts (sun_path).
	maxSocketPath = 107
	// DefaultTCPTimeout is a TCP check's connect timeout; slower links
	// set active.tcp.targets[].timeout.
	DefaultTCPTimeout = 750 * time.Millisecond
)

// Defaults returns the compiled defaults.
func Defaults() *Config {
	return &Config{
		Version: SchemaVersion,
		Passive: PassiveConfig{
			Protocols: PassiveProtocols{ARP: true, IPv4: true, IPv6: false, DHCP: true, MDNS: true, DNS: true, LLDP: true},
			RingSize:  2 << 20,
		},
		Neighbor: NeighborConfig{ResyncInterval: Duration(10 * time.Minute)},
		// Active discovery (ADR 0012): probe starts, not packets. Off until
		// an interface enables it; the TCP ports are suggestions that run
		// only once active.tcp.enabled is set.
		Active: ActiveConfig{
			StartupDelay:       Duration(30 * time.Second),
			Jitter:             0.1,
			MaxStartsPerSecond: 5,
			MaxConcurrentJobs:  4,
			MinTargetInterval:  Duration(5 * time.Second),
			Budgets: Budgets{
				ARP:   StartBudget{StartsPerSecond: 5},
				ICMP:  StartBudget{StartsPerSecond: 1},
				Names: StartBudget{StartsPerSecond: 1},
				TCP:   TCPBudget{ConnectsPerSecond: 1, MaxConcurrentPerInterface: 2, MaxConcurrentPerHost: 1},
			},
			MaxAutoScanPrefixV4: 24,
			MaxSweepTargets:     DefaultMaxSweepTargets,
			ARP:                 ProbeConfig{Enabled: true, Interval: Duration(15 * time.Minute)},
			ICMP:                ProbeConfig{Enabled: false, Interval: Duration(15 * time.Minute)},
			TCP: TCPProbeConfig{Enabled: false, Interval: Duration(15 * time.Minute), Targets: []TCPTarget{
				{Port: 80, Name: "http", Timeout: Duration(DefaultTCPTimeout)},
				{Port: 443, Name: "https", Timeout: Duration(DefaultTCPTimeout)},
				{Port: 8080, Name: "http-alt", Timeout: Duration(DefaultTCPTimeout)},
				{Port: 502, Name: "modbus", Timeout: Duration(DefaultTCPTimeout)},
			}},
			Names: NamesConfig{Enabled: false, Interval: Duration(30 * time.Minute), MDNS: true, MaxQueriesPerPass: 64},
		},
		Presence: PresenceConfig{
			Active: Duration(5 * time.Minute),
			Recent: Duration(30 * time.Minute),
			Stale:  Duration(24 * time.Hour),
		},
		Identity: IdentityConfig{
			HostnamePreference: []string{"mdns", "dhcp", "dns", "dns_ptr", "lldp"},
			AddressExpiry:      Duration(24 * time.Hour),
			NameExpiry:         Duration(168 * time.Hour),
			ProxyARPThreshold:  16,
		},
		Storage: StorageConfig{
			Path: DefaultDBPath,
			Retention: RetentionConfig{
				Observations: Duration(168 * time.Hour),
				Rollups:      Duration(2160 * time.Hour),
				Events:       Duration(17520 * time.Hour),
				MaxDBSize:    200 * 1000 * 1000,
			},
		},
		API:     APIConfig{Socket: DefaultSocketPath},
		Metrics: MetricsConfig{Enabled: true},
		Logging: LoggingConfig{Level: "info", Format: "journald"},
	}
}
