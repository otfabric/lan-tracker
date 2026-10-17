# ADR 0012: A connectivity tracker with history, without identification

- Status: Accepted
- Date: 16 Oct 2026
- Specified in: `REQUIREMENTS.md` §1, §4, §5 (Tracker scope, 16 Oct 2026); `ARCHITECTURE.md` §5; `DATA_MODEL.md` §2, §5; `docs/MIGRATION.md`; AGENTS.md rules 7 and 11
- Supersedes: [0011](0011-active-identification-probes.md) entirely; [0008](0008-active-discovery-safety-envelope.md) for its UDP probes and its packet-rate budget. The rest of 0008 stands.

## Context

LAN Sentinel (`boeboe/lan-sentinel`, baseline commit `a437a22`, 8 Oct 2026) grew device identification: payload probes (Modbus FC 43/14, HTTP, TLS, SNMPv2c, SSH banner, Telnet, FTP), UDP NTP and EtherNet/IP ListIdentity probes, and passive classifiers that guessed `device_type`, `os` and `model` with confidences. The maintainer narrowed the product (16 Oct 2026) and moved it to OT Fabric as `otfabric/lan-tracker`. What operators need is an evidence-based history of which MACs, IPs, names and TCP endpoints are visible on each local OT network, so they can find equipment, troubleshoot connectivity and reconstruct incidents. Protocol-specific investigation belongs to OT Fabric's separate tools (`modbusctl`, `sunspecctl`, `otprobe`), run by people.

## Decision

- **Purpose.** LAN Tracker records where a MAC is, which IPs and names it used, what DHCP told it, and whether configured TCP endpoints answered. It does not determine equipment models, operating systems, firmware, serial numbers or physical asset identity. An operator's description on the MAC (within its interface) is the only statement about what a device is.
- **Removed from the runtime.** The identification probes and their configuration, the UDP NTP and ListIdentity probes, the passive identifiers and their confidence and stickiness rules, `identify run`, and `VENDOR_IDENTIFIED` as an emitted event. Legacy identification rows and events stay readable as history; nothing new is written to them.
- **Kept.** Passive ARP, IPv4, IPv6/NDP (narrowly), DHCPv4, mDNS/DNS-SD, DNS and LLDP; the kernel neighbour table; the ARP sweep, optional ICMP echo and bare TCP connects; the OUI lookup as a MAC-prefix registrant hint, never a manufacturer; the proxy-ARP safeguard as a diagnostic flag; host descriptions.
- **Added narrowly.** Paced mDNS and DNS queries for names and addresses already known, plus explicitly configured DNS-SD types; deeper passive DHCPv4 decoding and a lease history. DHCP, DNS and mDNS are configuration and naming exchanges; they are not an exception for application identification traffic.
- **Rates are probe starts.** The public contract is application-initiated probe starts per second (one ARP request, ICMP request, name query or TCP connect initiation is one start), with separate TCP connection limits. Kernel-generated packets (handshake ACKs, retransmissions, the close) are bounded by timeouts and concurrency but not paced one by one, and no total Ethernet packet rate is promised.
- **Evidence stays typed.** A frame from a MAC, an address claim at a MAC, a DHCP server's assertion, an advertised name or address, an endpoint that answered, and a cached or last-known fact are different evidence. TCP/502 OPEN is not proof of Modbus, a PLC or an inverter.

## Consequences

- `internal/probe/idprobe`, `internal/probe/udp`, the passive identifiers in `internal/identify` and `internal/cli/identify.go` are deleted; the configuration moves to schema version 2, where removed keys are errors, and `config convert` translates a version-1 file without enabling probing.
- Migrations 0001–0009 are kept as released. Later migrations add focused tables and keep legacy rows; `identify_attempts` and `identifications` are no longer written.
- Agents must not restore identification under another name: a new payload-sending probe, a classifier, a fingerprint database or automatic invocation of external tools needs a new ADR.

## Ruled out

- Payload identification probes of any kind, banner reading and TLS handshakes; UDP probes other than DNS and mDNS queries.
- Passive device, OS or vendor classification, and confidence ranking.
- SYN scanning, generic UDP scanning, register scanning, IPv6 sweeping.
- A DHCP client, server, leasequery client or packet injector; DHCPv6.
- A UI, remote API, fleet sync, alerting engine, automatic network repair and continuous packet recording.
- Depending on `go-otfp` or running `modbusctl`, `sunspecctl` or `otprobe` from the daemon.
