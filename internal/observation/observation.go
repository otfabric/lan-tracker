// Package observation defines the one type every collector and probe emits
// (docs/DATA_MODEL.md §2), its JSONL encoding (replay files, golden
// scenarios) and the bounded bus that carries observations to the
// correlator.
package observation

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"time"
)

// Source says which mechanism produced an observation.
type Source string

// Sources (docs/DATA_MODEL.md §2).
const (
	PassiveARP  Source = "passive_arp"
	PassiveIPv4 Source = "passive_ipv4"
	PassiveIPv6 Source = "passive_ipv6"
	PassiveNDP  Source = "passive_ndp"
	PassiveDHCP Source = "passive_dhcp"
	// PassiveDHCPLease is a server's DHCPACK that allocates or renews a
	// lease (yiaddr); PassiveDHCPServer is the sender of a DHCP reply, the
	// server or the relay that forwarded it (docs/DATA_MODEL.md §5.6).
	PassiveDHCPLease  Source = "passive_dhcp_lease"
	PassiveDHCPServer Source = "passive_dhcp_server"
	PassiveMDNS       Source = "passive_mdns"
	PassiveDNS        Source = "passive_dns"
	PassiveLLDP       Source = "passive_lldp"
	KernelNeighbor    Source = "kernel_neighbor"
	ARPScan           Source = "arp_scan"
	ICMPScan          Source = "icmp_scan"
	NDPProbe          Source = "ndp_probe"
	TCPConnect        Source = "tcp_connect"
	// DNSQuery and MDNSQuery are the answers to LAN Tracker's own name
	// queries (FR-AC-12).
	DNSQuery  Source = "dns_query"
	MDNSQuery Source = "mdns_query"
)

// Sources lists every valid source.
var Sources = []Source{
	PassiveARP, PassiveIPv4, PassiveIPv6, PassiveNDP, PassiveDHCP, PassiveDHCPLease, PassiveDHCPServer, PassiveMDNS,
	PassiveDNS, PassiveLLDP, KernelNeighbor, ARPScan, ICMPScan, NDPProbe, TCPConnect, DNSQuery, MDNSQuery,
}

// Valid reports whether s is a known source.
func (s Source) Valid() bool { return slices.Contains(Sources, s) }

// NameType is the provenance of a hostname.
type NameType string

// Name types.
const (
	NameMDNS    NameType = "mdns"
	NameDHCP    NameType = "dhcp"
	NameDNS     NameType = "dns" // forward DNS: A, AAAA, CNAME
	NameDNSPTR  NameType = "dns_ptr"
	NameNetBIOS NameType = "netbios"
	NameLLDP    NameType = "lldp"
)

// RecordType is the kind of a name record (docs/DATA_MODEL.md §5.4).
type RecordType string

// Record types. NAME is a name a device gives itself outside DNS: the
// DHCP hostname or client FQDN in its own request, an LLDP system name.
const (
	RecordA     RecordType = "A"
	RecordAAAA  RecordType = "AAAA"
	RecordPTR   RecordType = "PTR"
	RecordSRV   RecordType = "SRV"
	RecordTXT   RecordType = "TXT"
	RecordCNAME RecordType = "CNAME"
	RecordName  RecordType = "NAME"
)

// RecordTypes lists every valid record type.
var RecordTypes = []RecordType{RecordA, RecordAAAA, RecordPTR, RecordSRV, RecordTXT, RecordCNAME, RecordName}

// Record is one advertised or resolved name record. It describes a name
// and, for A, AAAA and reverse PTR records, an address; it never says
// which MAC holds that address: the advertiser (the observation's sender)
// is kept apart from the endpoint it describes (docs/DATA_MODEL.md §5.4).
type Record struct {
	Name string     `json:"name"`
	Type RecordType `json:"type"`
	// Addr is the address an A, AAAA or reverse PTR record describes.
	Addr netip.Addr `json:"addr,omitzero"`
	// Value is a CNAME target, an SRV target as "host:port", TXT text or
	// the instance a service PTR names.
	Value string `json:"value,omitempty"`
	// TTL in seconds, nil when unknown. An mDNS TTL of 0 is a goodbye.
	TTL *uint32 `json:"ttl,omitempty"`
	// Flush is mDNS's cache-flush bit: this record replaces the sender's
	// other records of the name and type (RFC 6762 §10.2).
	Flush bool `json:"flush,omitempty"`
}

// ServiceState is a probe result.
type ServiceState string

// Probe results.
const (
	ServiceOpen        ServiceState = "OPEN"
	ServiceRefused     ServiceState = "REFUSED"
	ServiceTimeout     ServiceState = "TIMEOUT"
	ServiceUnreachable ServiceState = "UNREACHABLE"
	// ServiceLocalError: the probe could not be made here (no permission,
	// no usable source address, no direct route, a missing interface). It
	// is no evidence about the target.
	ServiceLocalError ServiceState = "LOCAL_ERROR"
)

// TTLOf returns a TTL pointer, for building records.
func TTLOf(seconds uint32) *uint32 { return &seconds }

// Answered reports the results that came from the endpoint: a completed
// connect or an active refusal (FR-AC-5). Only these are definite.
func (s ServiceState) Answered() bool { return s == ServiceOpen || s == ServiceRefused }

// MetaDetail is the reason of a LOCAL_ERROR result.
const MetaDetail = "detail"

// Meta keys of DHCP observations (docs/DATA_MODEL.md §2, §5.6): the
// message type, the server identifier (option 54, absent when missing or
// invalid), the relay address (giaddr), whether a relay agent information
// option (82) was present, the advertised router (3), DNS servers (6) and
// subnet mask (1), the lease time (51) and, on an ACK that allocates
// nothing (the answer to an INFORM), config_only.
const (
	MetaDHCP         = "dhcp"
	MetaServerID     = "server_id"
	MetaRelay        = "relay"
	MetaRelayAgent   = "relay_agent"
	MetaRouter       = "router"
	MetaDNS          = "dns"
	MetaSubnetMask   = "subnet_mask"
	MetaLeaseSeconds = "lease_seconds"
	MetaConfigOnly   = "config_only"
)

// Evidence is what an observation establishes about its MAC
// (docs/DATA_MODEL.md §2). The kinds stay apart: only a frame, an address
// confirmation and an answered probe make a host present.
type Evidence int

// Evidence kinds.
const (
	// EvidenceNone: the observation says nothing about any MAC's presence
	// (a DNS answer, a TCP timeout, a local probe failure).
	EvidenceNone Evidence = iota
	// EvidenceFrame: a frame came from this Ethernet source MAC.
	EvidenceFrame
	// EvidenceAddress: an address was confirmed at this MAC (an ARP
	// answer, a REACHABLE neighbour entry).
	EvidenceAddress
	// EvidenceAnswer: the endpoint at the IP answered (TCP OPEN or
	// REFUSED, an ICMP echo reply); it is the holder's only when exactly
	// one MAC holds the IP.
	EvidenceAnswer
	// EvidenceCached: a cache's or a server's statement about the MAC (a
	// STALE neighbour entry, a DHCP server's lease assertion). It may
	// record a MAC that was never seen, but never refreshes one.
	EvidenceCached
)

// Evidence classifies o.
func (o Observation) Evidence() Evidence {
	switch o.Source {
	case PassiveDNS, DNSQuery, MDNSQuery:
		if len(o.MAC) == 0 {
			return EvidenceNone
		}
		return EvidenceFrame // an mDNS answer to our query still came from its sender
	case PassiveDHCPLease:
		return EvidenceCached // the server's statement; the client may never have accepted it
	case KernelNeighbor:
		if o.NeighborState == "REACHABLE" || o.NeighborState == "" {
			return EvidenceAddress
		}
		return EvidenceCached // STALE, DELAY, PROBE: the kernel has not confirmed it lately
	case PassiveARP, ARPScan, PassiveNDP, NDPProbe:
		if len(o.MAC) > 0 {
			return EvidenceAddress
		}
	case TCPConnect, ICMPScan:
		if len(o.MAC) == 0 {
			if o.Service != nil && !o.Service.State.Answered() {
				return EvidenceNone
			}
			return EvidenceAnswer
		}
	}
	if len(o.MAC) > 0 {
		return EvidenceFrame
	}
	return EvidenceNone
}

// Present reports evidence that refreshes presence.
func (e Evidence) Present() bool {
	return e == EvidenceFrame || e == EvidenceAddress || e == EvidenceAnswer
}

// ServiceResult is the outcome of probing one protocol/port.
type ServiceResult struct {
	Proto string       `json:"proto"`
	Port  int          `json:"port"`
	State ServiceState `json:"state"`
}

// Key is "proto/port", e.g. "tcp/502".
func (s ServiceResult) Key() string { return fmt.Sprintf("%s/%d", s.Proto, s.Port) }

// Observation is raw evidence: source S saw MAC X / IP Y / name Z at time T
// on interface I.
type Observation struct {
	Time          time.Time
	Source        Source
	Interface     string           // network context key
	MAC           net.HardwareAddr // nil if the source has none (e.g. TCP probe)
	IP            netip.Addr       // invalid if the source has none (e.g. LLDP)
	Hostname      string
	NameType      NameType
	Service       *ServiceResult
	NeighborState string // kernel_neighbor: NUD state
	// Records are the name records the observation carries (mDNS, DNS).
	Records []Record
	// DHCP is the decoded DHCPv4 message of a passive_dhcp or
	// passive_dhcp_server observation.
	DHCP *DHCPMessage
	Meta map[string]string
}

// jsonObservation is the JSONL wire form (docs/DATA_MODEL.md §2).
type jsonObservation struct {
	Time          time.Time         `json:"time"`
	Source        Source            `json:"source"`
	Interface     string            `json:"interface,omitempty"`
	MAC           string            `json:"mac,omitempty"`
	IP            string            `json:"ip,omitempty"`
	Hostname      string            `json:"hostname,omitempty"`
	NameType      NameType          `json:"name_type,omitempty"`
	Service       *ServiceResult    `json:"service,omitempty"`
	NeighborState string            `json:"neighbor_state,omitempty"`
	Records       []Record          `json:"records,omitempty"`
	DHCP          *DHCPMessage      `json:"dhcp,omitempty"`
	Meta          map[string]string `json:"meta,omitempty"`
}

// MarshalJSON implements json.Marshaler.
func (o Observation) MarshalJSON() ([]byte, error) {
	j := jsonObservation{
		Time: o.Time.UTC(), Source: o.Source, Interface: o.Interface, Hostname: o.Hostname,
		NameType: o.NameType, Service: o.Service, NeighborState: o.NeighborState, Records: o.Records, DHCP: o.DHCP, Meta: o.Meta,
	}
	if len(o.MAC) > 0 {
		j.MAC = o.MAC.String()
	}
	if o.IP.IsValid() {
		j.IP = o.IP.String()
	}
	return json.Marshal(j)
}

// UnmarshalJSON implements json.Unmarshaler. Unknown fields are rejected.
func (o *Observation) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var j jsonObservation
	if err := dec.Decode(&j); err != nil {
		return err
	}
	if j.Time.IsZero() {
		return errors.New("missing time")
	}
	if !j.Source.Valid() {
		return fmt.Errorf("unknown source %q", j.Source)
	}
	out := Observation{
		Time: j.Time, Source: j.Source, Interface: j.Interface, Hostname: j.Hostname, NameType: j.NameType,
		Service: j.Service, NeighborState: j.NeighborState, Records: j.Records, DHCP: j.DHCP, Meta: j.Meta,
	}
	for i, r := range out.Records {
		if !slices.Contains(RecordTypes, r.Type) || r.Name == "" {
			return fmt.Errorf("records[%d]: want a name and a type of %v", i, RecordTypes)
		}
	}
	if j.MAC != "" {
		mac, err := net.ParseMAC(j.MAC)
		if err != nil {
			return fmt.Errorf("mac: %w", err)
		}
		out.MAC = mac
	}
	if j.IP != "" {
		ip, err := netip.ParseAddr(j.IP)
		if err != nil {
			return fmt.Errorf("ip: %w", err)
		}
		out.IP = ip.Unmap()
	}
	if out.MAC == nil && !out.IP.IsValid() && out.Hostname == "" && len(out.Records) == 0 {
		return errors.New("observation has neither mac, ip, hostname nor records")
	}
	*o = out
	return nil
}

// maxLine bounds one JSONL line.
const maxLine = 1 << 20

// ReadJSONL calls fn for every observation in r, one JSON object per line.
// Blank lines are skipped; errors name the line.
func ReadJSONL(r io.Reader, fn func(Observation) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	for line := 1; sc.Scan(); line++ {
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var o Observation
		if err := json.Unmarshal(b, &o); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
		if err := fn(o); err != nil {
			return fmt.Errorf("line %d: %w", line, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	return nil
}
