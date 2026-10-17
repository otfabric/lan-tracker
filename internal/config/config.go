// Package config defines the LAN Tracker configuration schema (version 2)
// and loads it from compiled defaults, the YAML file, LAN_TRACKER_*
// environment variables and command-line flags, in that order of
// increasing precedence, with strict validation. See docs/ARCHITECTURE.md
// §6. Convert converts a LAN Sentinel version-1 file (convert.go).
package config

import (
	"net/netip"
	"slices"
)

// SchemaVersion is the configuration schema this binary reads. Version 1
// was LAN Sentinel's; `lan-tracker config convert` converts it.
const SchemaVersion = 2

// Config is the complete configuration.
type Config struct {
	Version    int                      `yaml:"version" json:"version"`
	Interfaces []InterfaceConfig        `yaml:"interfaces" json:"interfaces"`
	Passive    PassiveConfig            `yaml:"passive" json:"passive"`
	Neighbor   NeighborConfig           `yaml:"neighbor" json:"neighbor"`
	Active     ActiveConfig             `yaml:"active" json:"active"`
	Profiles   map[string]ProfileConfig `yaml:"profiles" json:"profiles"`
	Presence   PresenceConfig           `yaml:"presence" json:"presence"`
	Identity   IdentityConfig           `yaml:"identity" json:"identity"`
	Storage    StorageConfig            `yaml:"storage" json:"storage"`
	API        APIConfig                `yaml:"api" json:"api"`
	Metrics    MetricsConfig            `yaml:"metrics" json:"metrics"`
	Logging    LoggingConfig            `yaml:"logging" json:"logging"`
	Replay     ReplayConfig             `yaml:"replay" json:"replay"`
}

// InterfaceConfig configures one monitored interface (one network context).
type InterfaceConfig struct {
	Name    string           `yaml:"name" json:"name"`
	Passive InterfacePassive `yaml:"passive" json:"passive"`
	Active  InterfaceActive  `yaml:"active" json:"active"`
	DHCP    InterfaceDHCP    `yaml:"dhcp" json:"dhcp"`
	// Prefixes and Replay are only used for replay interfaces, whose subnets
	// cannot come from the interface manager.
	Prefixes []netip.Prefix   `yaml:"prefixes,omitempty" json:"prefixes,omitempty"`
	Replay   *InterfaceReplay `yaml:"replay,omitempty" json:"replay,omitempty"`
}

// IsReplay reports whether the interface is fed from a replay file.
func (i InterfaceConfig) IsReplay() bool { return i.Replay != nil }

// ReplayMode reports whether the daemon replays recorded files (every
// interface is a replay interface; validation forbids mixing).
func (c *Config) ReplayMode() bool {
	return len(c.Interfaces) > 0 && c.Interfaces[0].IsReplay()
}

// PassiveEnabled reports whether passive capture is enabled. It defaults to
// true for live interfaces and false for replay interfaces.
func (i InterfaceConfig) PassiveEnabled() bool {
	if i.Passive.Enabled == nil {
		return !i.IsReplay()
	}
	return *i.Passive.Enabled
}

// InterfacePassive configures passive capture on one interface.
type InterfacePassive struct {
	Enabled     *bool `yaml:"enabled" json:"enabled"`
	Promiscuous bool  `yaml:"promiscuous" json:"promiscuous"`
}

// InterfaceActive configures active discovery on one interface.
type InterfaceActive struct {
	Enabled  bool           `yaml:"enabled" json:"enabled"`
	Networks []netip.Prefix `yaml:"networks,omitempty" json:"networks,omitempty"`
	Exclude  []AddrOrPrefix `yaml:"exclude,omitempty" json:"exclude,omitempty"`
}

// InterfaceDHCP configures DHCP server monitoring on one interface
// (docs/DATA_MODEL.md §5.6).
type InterfaceDHCP struct {
	// Servers lists the DHCP server identifiers (option 54) allowed on the
	// interface; a reply from any other, or without a valid identifier, is
	// unexpected. Unset: no allowlist, servers are reported without a
	// verdict. Empty: no DHCP server is expected.
	Servers *[]netip.Addr `yaml:"servers,omitempty" json:"servers,omitempty"`
}

// Checked reports whether the interface has an allowlist.
func (d InterfaceDHCP) Checked() bool { return d.Servers != nil }

// Allowed reports whether a server identifier is on the allowlist; an
// invalid (unknown) identifier never is.
func (d InterfaceDHCP) Allowed(serverID netip.Addr) bool {
	return d.Servers != nil && serverID.IsValid() && slices.Contains(*d.Servers, serverID)
}

// InterfaceReplay feeds an interface from a recorded file.
type InterfaceReplay struct {
	File  string  `yaml:"file" json:"file"`
	Speed float64 `yaml:"speed" json:"speed"`
}

// PassiveConfig holds global passive-capture settings.
type PassiveConfig struct {
	Protocols PassiveProtocols `yaml:"protocols" json:"protocols"`
	// RingSize is the AF_PACKET ring buffer per captured interface.
	RingSize ByteSize `yaml:"ring_size" json:"ring_size"`
}

// PassiveProtocols enables individual decoders.
type PassiveProtocols struct {
	ARP  bool `yaml:"arp" json:"arp"`
	IPv4 bool `yaml:"ipv4" json:"ipv4"`
	IPv6 bool `yaml:"ipv6" json:"ipv6"`
	DHCP bool `yaml:"dhcp" json:"dhcp"`
	MDNS bool `yaml:"mdns" json:"mdns"`
	DNS  bool `yaml:"dns" json:"dns"`
	LLDP bool `yaml:"lldp" json:"lldp"`
}

// NeighborConfig configures the kernel neighbour collector.
type NeighborConfig struct {
	ResyncInterval Duration `yaml:"resync_interval" json:"resync_interval"`
}

// ActiveConfig holds global active-discovery settings and safety budgets.
// Rates are probe starts per second (ADR 0012): one ARP request, ICMP echo,
// name query or TCP connect initiation is one start; the kernel's own
// packets (handshake ACKs, retransmissions, the close) are not paced one by
// one.
type ActiveConfig struct {
	StartupDelay       Duration `yaml:"startup_delay" json:"startup_delay"`
	Jitter             float64  `yaml:"jitter" json:"jitter"`
	MaxStartsPerSecond float64  `yaml:"max_starts_per_second" json:"max_starts_per_second"`
	// MaxConcurrentJobs caps the probes in flight across every engine and
	// interface, name queries included.
	MaxConcurrentJobs   int      `yaml:"max_concurrent_jobs" json:"max_concurrent_jobs"`
	MinTargetInterval   Duration `yaml:"min_target_interval" json:"min_target_interval"`
	Budgets             Budgets  `yaml:"budgets" json:"budgets"`
	MaxAutoScanPrefixV4 int      `yaml:"max_auto_scan_prefix_v4" json:"max_auto_scan_prefix_v4"`
	AllowWideScan       bool     `yaml:"allow_wide_scan" json:"allow_wide_scan"`
	// MaxSweepTargets caps the addresses one interface's ARP sweep covers
	// (after excludes). Above DefaultMaxSweepTargets it is an expert
	// override: it needs allow_wide_scan and rates and concurrency at or
	// below their defaults.
	MaxSweepTargets int            `yaml:"max_sweep_targets" json:"max_sweep_targets"`
	ARP             ProbeConfig    `yaml:"arp" json:"arp"`
	ICMP            ProbeConfig    `yaml:"icmp" json:"icmp"`
	TCP             TCPProbeConfig `yaml:"tcp" json:"tcp"`
	Names           NamesConfig    `yaml:"names" json:"names"`
}

// NamesConfig configures the name queries (FR-AC-12): they ask only about
// names and addresses already known, and DNS-SD types the operator names,
// on interfaces with active discovery enabled.
type NamesConfig struct {
	Enabled  bool     `yaml:"enabled" json:"enabled"`
	Interval Duration `yaml:"interval" json:"interval"`
	// MDNS sends one-shot mDNS queries for the interface's known .local
	// names and for ServiceTypes.
	MDNS bool `yaml:"mdns" json:"mdns"`
	// ServiceTypes are DNS-SD types to enumerate, e.g. _http._tcp.
	ServiceTypes []string `yaml:"service_types" json:"service_types"`
	// EnumerateServices adds one _services._dns-sd._udp.local query per
	// pass; the types it returns are not queried automatically.
	EnumerateServices bool `yaml:"enumerate_services" json:"enumerate_services"`
	// Resolvers are the unicast DNS servers asked for PTR records of known
	// addresses and A/AAAA records of known DNS names; none: no unicast
	// DNS queries. Observed traffic is never sent to other resolvers.
	Resolvers []netip.Addr `yaml:"resolvers" json:"resolvers"`
	// MaxQueriesPerPass bounds one pass on one interface.
	MaxQueriesPerPass int `yaml:"max_queries_per_pass" json:"max_queries_per_pass"`
}

// Budgets are the per-protocol start rates beneath the global one.
type Budgets struct {
	ARP   StartBudget `yaml:"arp" json:"arp"`
	ICMP  StartBudget `yaml:"icmp" json:"icmp"`
	Names StartBudget `yaml:"names" json:"names"`
	TCP   TCPBudget   `yaml:"tcp" json:"tcp"`
}

// StartBudget limits how many probes of one protocol start per second.
type StartBudget struct {
	StartsPerSecond float64 `yaml:"starts_per_second" json:"starts_per_second"`
}

// TCPBudget limits the TCP connect engine. A connect is one start of the
// global budget.
type TCPBudget struct {
	ConnectsPerSecond         float64 `yaml:"connects_per_second" json:"connects_per_second"`
	MaxConcurrentPerInterface int     `yaml:"max_concurrent_per_interface" json:"max_concurrent_per_interface"`
	MaxConcurrentPerHost      int     `yaml:"max_concurrent_per_host" json:"max_concurrent_per_host"`
}

// ProbeConfig enables a periodic probe.
type ProbeConfig struct {
	Enabled  bool     `yaml:"enabled" json:"enabled"`
	Interval Duration `yaml:"interval" json:"interval"`
}

// TCPProbeConfig configures periodic TCP connect probes.
type TCPProbeConfig struct {
	Enabled  bool        `yaml:"enabled" json:"enabled"`
	Interval Duration    `yaml:"interval" json:"interval"`
	Targets  []TCPTarget `yaml:"targets" json:"targets"`
}

// TCPTarget is one probed TCP port.
type TCPTarget struct {
	Port    int      `yaml:"port" json:"port"`
	Name    string   `yaml:"name" json:"name"`
	Timeout Duration `yaml:"timeout" json:"timeout"`
}

// ProfileConfig is a named on-demand scan profile.
type ProfileConfig struct {
	ARP  bool  `yaml:"arp" json:"arp"`
	ICMP bool  `yaml:"icmp" json:"icmp"`
	TCP  []int `yaml:"tcp" json:"tcp"`
}

// PresenceConfig holds presence thresholds (time since last observation).
type PresenceConfig struct {
	Active Duration `yaml:"active" json:"active"`
	Recent Duration `yaml:"recent" json:"recent"`
	Stale  Duration `yaml:"stale" json:"stale"`
}

// IdentityConfig holds correlation and naming settings. Addresses of one
// MAC are independent: a new address never closes another (there is no
// overlap window); an address not confirmed for AddressExpiry while the
// host was seen is closed as an inference (docs/DATA_MODEL.md §5.3).
type IdentityConfig struct {
	HostnamePreference []string `yaml:"hostname_preference" json:"hostname_preference"`
	AddressExpiry      Duration `yaml:"address_expiry" json:"address_expiry"`
	// NameExpiry marks a name not confirmed for this long, and without a
	// record TTL, as stale when it is read; it never closes the name
	// (docs/DATA_MODEL.md §5.4).
	NameExpiry        Duration `yaml:"name_expiry" json:"name_expiry"`
	ProxyARPThreshold int      `yaml:"proxy_arp_threshold" json:"proxy_arp_threshold"`
	OUIOverride       string   `yaml:"oui_override" json:"oui_override"`
}

// StorageConfig configures the SQLite database.
type StorageConfig struct {
	Path      string          `yaml:"path" json:"path"`
	Retention RetentionConfig `yaml:"retention" json:"retention"`
}

// RetentionConfig holds retention limits.
type RetentionConfig struct {
	Observations Duration `yaml:"observations" json:"observations"`
	Rollups      Duration `yaml:"rollups" json:"rollups"`
	Events       Duration `yaml:"events" json:"events"`
	MaxDBSize    ByteSize `yaml:"max_db_size" json:"max_db_size"`
}

// APIConfig configures the local API listeners.
type APIConfig struct {
	Socket string `yaml:"socket" json:"socket"`
	Listen string `yaml:"listen" json:"listen"`
}

// MetricsConfig enables /metrics on the local TCP listener.
type MetricsConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
}

// LoggingConfig configures logging.
type LoggingConfig struct {
	Level  string `yaml:"level" json:"level"`
	Format string `yaml:"format" json:"format"`
}

// ReplayConfig holds global replay settings.
type ReplayConfig struct {
	ExitWhenDone bool `yaml:"exit_when_done" json:"exit_when_done"`
}
