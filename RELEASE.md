# lan-tracker Releases

## v0.1.0

**Date:** 2026-10-17

### Summary

First release of **OT Fabric LAN Tracker**, a **connectivity tracker with history** for local OT networks ([ADR 0011](docs/adr/0011-connectivity-tracker-scope.md)). It records which MAC held which IP on which interface and when, the names advertised or resolved for them, what DHCP servers acknowledged to each client, whether the configured TCP ports answered, and when the collector could not see. It is not an identification product: it does not determine equipment models, operating systems or firmware, and the operator's description on a MAC is the only statement about what a device is. It is one static binary, `lan-tracker`, run under systemd on Linux edge devices; [`deploy/README.md`](deploy/README.md) has the install, upgrade and rollout runbook.

### Highlights

- **Names and paths:** binary `lan-tracker`, unit `lan-tracker.service`, `/etc/lan-tracker/config.yaml` (`version: 1`), `/data/lan-tracker/hosts.db`, `/run/lan-tracker/api.sock`, environment `LAN_TRACKER_*`, metrics `lan_tracker_*`, module `github.com/otfabric/lan-tracker`.
- **Passive by default:** a receive-only capture per interface (ARP, DHCP, mDNS, DNS responses, LLDP and, when enabled, NDP) and the kernel neighbour table; nothing is sent until active discovery is enabled on an interface with explicit networks.
- **Host = MAC per interface:** hosts are never merged or split; addresses, names, DHCP leases and TCP endpoints are attributes with first and last seen.
- **Typed evidence:** a frame from a MAC, an address confirmed at it and an endpoint that answered refresh presence; a DHCP server's assertion, a STALE neighbour entry, a DNS answer, a timeout or silence never do.
- **Point-in-time queries:** `hosts find --at` answers who held an IP at a moment, with conflicts, the binding that replaced it and the MAC's next new address (`next_address`); `hosts history` gives an IP's ownership timeline.
- **TCP endpoints per address and port**, with result history (`services list --at`), the last definite answer kept apart from timeouts, `LOCAL_ERROR` with its reason, and a kernel route check before every connect: nothing is dialled without a direct route and a source address on the interface. A connect sends no payload.
- **Name records** from mDNS, DNS, DHCP and LLDP with record type, advertiser, TTL and expiry, linked to a MAC only by evidence; an mDNS proxy binds nothing to itself.
- **Name queries** (`active.names`, off by default): one-shot mDNS and unicast DNS to configured resolvers, only about what is already known, within their own start budget.
- **DHCPv4 in depth:** the whole message decoded (options split or overloaded, FQDN, relay agent information, search lists, classless routes); the servers and relays that answer, checked against an optional per-interface allowlist (`DHCP_SERVER_UNEXPECTED`); each client's exchanges with offers, choice and outcome (`dhcp exchanges`); a per-client lease history with configuration snapshots, the gateway option 121 implies, renewals counted and changes as `DHCP_LEASE_ASSIGNED` and `DHCP_CLIENT_CONFIG_CHANGED`, NAK, DECLINE and RELEASE events (`dhcp leases`, a host's leases in `hosts show`). A lease never creates a host or makes its client present.
- **Events** for state transitions only (`IP_ADDED`, `IP_REMOVED`, `HOSTNAME_ADDED`, `HOSTNAME_REMOVED`, `MAC_ALSO_OBSERVED`, `DUPLICATE_IP_DETECTED` and `DUPLICATE_IP_RESOLVED`, `SERVICE_STATE_CHANGED`, `PROXY_ARP_DETECTED`, the DHCP events and others), in the database and journald.
- **Collection gaps:** `OBSERVATION_GAP` when a passive collector stops and recovers, and for the daemon's own downtime at the next start.
- **OT safety envelope:** rates are probe starts per second. Defaults: 5 starts/s in all; ARP 5, ICMP 1, name queries 1, TCP 1 connect/s (2 at once per interface, 1 per host); 4 probes in flight; 5 s between probes to one target; sweeps and TCP checks every 15 min; TCP connect timeout 750 ms; at most 1,024 addresses per sweep. Excludes and a persistent kill switch (`active disable`, `LAN_TRACKER_ACTIVE_DISABLED=1`). No SYN, UDP port or IPv6 scanning.
- **`scan plan`** simulates concurrency slots and phases, so its duration estimate matches a run; known hosts without a direct route are listed and left out.
- **Operations:** a local REST API on a Unix socket, Prometheus metrics without high-cardinality labels, `--offline` reads of the database when the daemon is down, `config reload` that is all or nothing and says what needs a restart, size-bounded retention that prunes history before the events that explain it, and a hardened systemd unit running as root with `CAP_NET_RAW` only.
- MIT licence, security policy and contribution guide; the licence ships in the release tarballs. Release notes come from this file: the release workflow refuses a version without its section.

### Tests

- `make check`: race tests and coverage of `internal/` above 90%; `make fuzz` over every decoder, including the DHCP decoder.
- `make test-net`, as uid 65534 with only `CAP_NET_RAW`: start budgets measured on the wire, no payload, name queries only to the mDNS group and the configured resolver.
- `make test-systemd` on Debian 11, 12 and 13 (`systemd-analyze security` exposure 2.3).
- `make soak`: 90 simulated days of a 50-host LAN with name records, TCP endpoints and DHCP exchanges.
- The golden reconstruction scenario (`docs/DATA_MODEL.md` §10) and kill -9 crash tests during writes.

### Not included

- Board and field validation (Raspberry Pi CM4, RevPi Connect, amd64 edge boxes) and the seven-day active pilot: [`docs/TEST_PLAN.md`](docs/TEST_PLAN.md).
- DHCPv6, IPv6 active probing, VLAN tagging, a web UI, alerting and any data leaving the box.
- Device identification of any kind (ADR 0011).
