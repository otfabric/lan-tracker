# ADR 0011: A connectivity tracker with history, without identification

- Status: Accepted
- Date: 16 Oct 2026
- Specified in: `REQUIREMENTS.md` §1, §4, §5 (Tracker scope, 16 Oct 2026); `ARCHITECTURE.md` §5; `DATA_MODEL.md` §2, §5; AGENTS.md rules 7 and 11
- Amends: [0008](0008-active-discovery-safety-envelope.md), whose budgets count probe starts as decided here

## Context

Operators of OT sites need an evidence-based history of which MACs, IPs, names and TCP endpoints are visible on each local network, so they can find equipment, troubleshoot connectivity and reconstruct incidents. Working out what a device is (its model, operating system or firmware) takes payload probes, banner reading or classifiers. Those send traffic that fragile OT equipment may not tolerate, and their guesses carry a confidence that a reconstruction cannot audit. Protocol-specific investigation belongs to OT Fabric's separate tools (`modbusctl`, `sunspecctl`, `otprobe`), run by people.

## Decision

- **Purpose.** LAN Tracker records where a MAC is, which IPs and names it used, what DHCP told it, and whether configured TCP endpoints answered. It does not determine equipment models, operating systems, firmware, serial numbers or physical asset identity. An operator's description on the MAC (within its interface) is the only statement about what a device is.
- **What it collects.** Passive ARP, IPv4, IPv6/NDP (narrowly), DHCPv4, mDNS/DNS-SD, DNS and LLDP; the kernel neighbour table; the ARP sweep, optional ICMP echo and bare TCP connects; paced mDNS and DNS queries for names and addresses already known, plus explicitly configured DNS-SD types; full passive DHCPv4 decoding with exchanges and a per-client lease history. DHCP, DNS and mDNS are configuration and naming exchanges; they are not an exception for application identification traffic.
- **What it shows about a MAC.** The OUI lookup as a MAC-prefix registrant hint, never a manufacturer; the proxy-ARP safeguard as a diagnostic flag; the operator's description.
- **Rates are probe starts.** The public contract is application-initiated probe starts per second (one ARP request, ICMP request, name query or TCP connect initiation is one start), with separate TCP connection limits. Kernel-generated packets (handshake ACKs, retransmissions, the close) are bounded by timeouts and concurrency but not paced one by one, and no total Ethernet packet rate is promised.
- **Evidence stays typed.** A frame from a MAC, an address claim at a MAC, a DHCP server's assertion, an advertised name or address, an endpoint that answered, and a cached or last-known fact are different evidence. TCP/502 OPEN is not proof of Modbus, a PLC or an inverter.

## Consequences

- There is no identification code to extend: `internal/identify` holds the OUI lookup and MAC helpers only, the schema has no device-type, manufacturer or identification tables, and the configuration has no identification or UDP probe keys (unknown keys are errors).
- The budgets, `scan plan` and `make test-net` speak in probe starts; the kernel's packets are measured apart.
- Agents must not add identification under another name: a new payload-sending probe, a classifier, a fingerprint database or automatic invocation of external tools needs a new ADR.

## Ruled out

- Payload identification probes of any kind, banner reading and TLS handshakes; UDP probes other than DNS and mDNS queries.
- Passive device, OS or vendor classification, and confidence ranking.
- SYN scanning, generic UDP scanning, register scanning, IPv6 sweeping.
- A DHCP client, server, leasequery client or packet injector; DHCPv6.
- A UI, remote API, fleet sync, alerting engine, automatic network repair and continuous packet recording.
- Depending on `go-otfp` or running `modbusctl`, `sunspecctl` or `otprobe` from the daemon.
