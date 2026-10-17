# lan-tracker Releases

## v0.1.0

**Date:** 2026-10-17
**Previous release:** LAN Sentinel v0.0.9 ([`boeboe/lan-sentinel`](https://github.com/boeboe/lan-sentinel))

### Summary

First release of **OT Fabric LAN Tracker**, the successor of LAN Sentinel, narrowed to a **connectivity tracker with history** ([ADR 0012](docs/adr/0012-connectivity-tracker-scope.md)). It records which MAC held which IP on which interface and when, the names advertised or resolved for them, what DHCP servers acknowledged to each client, whether the configured TCP ports answered, and when the collector could not see. **Device identification is removed**: no payload probes, no device type, OS or model classification; the operator's description on a MAC is the only statement about what a device is. A LAN Sentinel database upgrades in place on a copy, keeping host IDs, descriptions, bindings, events and the kill switch; [`deploy/README.md`](deploy/README.md) has the cutover and rollback runbook and [`docs/MIGRATION.md`](docs/MIGRATION.md) the full change log.

### Breaking changes (from LAN Sentinel)

- **Names and paths:** binary `lan-tracker`, unit `lan-tracker.service`, `/etc/lan-tracker/config.yaml`, `/data/lan-tracker/hosts.db`, `/run/lan-tracker/api.sock`, environment `LAN_TRACKER_*`, metrics `lan_tracker_*`, module `github.com/otfabric/lan-tracker`.
- **Configuration schema 2** (`version: 2`). Schema 1, removed keys and any `LAN_SENTINEL_*` variable are refused. `lan-tracker config convert FILE` prints the converted file and a report of what to review; it leaves active discovery off on every interface.
- **Rates are probe starts per second**, not packet tokens. Defaults: 5 starts/s in all; ARP 5, ICMP 1, name queries 1, TCP 1 connect/s (2 at once per interface, 1 per host); 4 probes in flight; 5 s between probes to one target; sweeps and TCP checks every 15 min; TCP connect timeout 750 ms; at most 1,024 addresses per sweep.
- **Removed:** `identify run`, `POST /v1/hosts/{id}/identify`, `hosts list --device`, the device type and manufacturer fields, UDP (NTP, EtherNet/IP) and identification probes, the `udp_probe` and `identify_probe` sources and the `lan_tracker_identify_*` metrics. The OUI lookup is now `oui_registrant`: a hint about the MAC prefix, never the maker.
- **Events:** `IP_CHANGED`, `MAC_MOVED`, `HOSTNAME_CHANGED`, `SERVICE_OPENED`, `SERVICE_CLOSED`, `VENDOR_IDENTIFIED`, `DHCP_CONFIG_CHANGED` and `IDENTIFY_RAN` are no longer emitted; LAN Sentinel's stay in history with their wording and remain valid filters. A MAC's addresses are independent (`IP_ADDED`, `IP_REMOVED`), names are records (`HOSTNAME_ADDED`, `HOSTNAME_REMOVED`), a MAC on a second interface is `MAC_ALSO_OBSERVED`, endpoints change with `SERVICE_STATE_CHANGED`.
- **JSON:** `hosts find --at` reports `next_address` instead of `moved_to`; `services list` returns one row per address and port with the last result and the last definite answer apart; hosts carry the evidence kinds (`last_frame_seen`, `last_address_confirmed`, `last_endpoint_answer`) and `dhcp_leases`.

### Added

- **Typed evidence:** a frame from a MAC, an address confirmed at it and an endpoint that answered refresh presence; a DHCP server's assertion, a STALE neighbour entry, a DNS answer, a timeout or silence never do.
- **TCP endpoints per address and port**, with result history (`services list --at`), `LOCAL_ERROR` with its reason, and a kernel route check before every connect: nothing is dialled without a direct route and a source address on the interface.
- **Name records** from mDNS, DNS, DHCP and LLDP with record type, advertiser, TTL and expiry, linked to a MAC only by evidence; an mDNS proxy binds nothing to itself.
- **Name queries** (`active.names`, off by default): one-shot mDNS and unicast DNS to configured resolvers, only about what is already known, within their own start budget.
- **DHCPv4 in depth:** the whole message decoded (options split or overloaded, FQDN, relay agent information, search lists, classless routes); each client's exchanges with offers, choice and outcome (`dhcp exchanges`); a per-client lease history with configuration snapshots, the gateway option 121 implies, renewals counted and changes as `DHCP_LEASE_ASSIGNED` and `DHCP_CLIENT_CONFIG_CHANGED`, NAK, DECLINE and RELEASE events (`dhcp leases`, a host's leases in `hosts show`). A lease never creates a host or makes its client present. LAN Sentinel's lease observations are backfilled.
- **Collection gaps:** `OBSERVATION_GAP` when a passive collector stops and recovers, and for the daemon's own downtime at the next start.
- **`scan plan`** simulates concurrency slots and phases, so its duration estimate matches a run; known hosts without a direct route are listed and left out.
- **Proxy ARP** as a host flag (`PROXY_ARP_DETECTED`).
- **API:** `/v1/dhcp/leases`, `/v1/dhcp/exchanges`; `description` filter on `/v1/hosts`.
- MIT licence, security policy and contribution guide; the licence ships in the release tarballs.

### Changed

- A MAC-less result (a TCP answer, an address record) is attributed only to the address's sole holder whose binding was confirmed within `identity.address_expiry`.
- A takeover that ends a duplicate-IP conflict emits `DUPLICATE_IP_RESOLVED`.
- Under `storage.retention.max_db_size`, history (exchanges, endpoint results, ended name records and leases) is pruned before the events that explain it; open leases are never pruned.
- `identity.oui_override` needs a restart; `config reload` says so.
- Release notes come from this file: the release workflow refuses a version without its section.

### Upgrading

Follow **Migrating from LAN Sentinel** in [`deploy/README.md`](deploy/README.md): stage the binary and the converted configuration, stop LAN Sentinel, copy its database to `/data/lan-tracker/`, start LAN Tracker passive and check the history, then enable the reviewed active profile. LAN Sentinel's database is left untouched for rollback; LAN Sentinel cannot open the migrated copy.

### Not included

- Board and field validation (Raspberry Pi CM4, RevPi Connect, amd64 edge boxes) and the seven-day active pilot: [`docs/TEST_PLAN.md`](docs/TEST_PLAN.md).
- DHCPv6, IPv6 active probing, VLAN tagging, a web UI, alerting and any data leaving the box.

### Tests

- `make check`: race tests and coverage of `internal/` above 90%; `make fuzz` over every decoder, including the new DHCP decoder.
- `make test-net`, as uid 65534 with only `CAP_NET_RAW`: start budgets measured on the wire, no payload, name queries only to the mDNS group and the configured resolver.
- `make test-systemd` on Debian 11, 12 and 13 (`systemd-analyze security` exposure 2.3).
- `make soak`: 90 simulated days of a 50-host LAN with name records, TCP endpoints and DHCP exchanges.
- `TestLANSentinelUpgrade`: a schema-0009 database with LAN Sentinel's rows upgrades without losing IDs, descriptions or history and without invented evidence.
