# lan-tracker Releases

## v0.2.2

**Date:** 2026-10-19
**Previous release:** v0.2.1

### Summary

Patch release: the systemd unit no longer logs `Unknown key name 'PrivateIPC'` on Debian 11 (systemd 247), and the sandbox is the same on every supported Debian. No change to the configuration, the schema, the CLI or the API.

### Fixed

- **`PrivateIPC=` was ignored with a warning on Debian 11.** The setting appeared in systemd 248, so on systemd 247 the daemon ran without it. The unit now denies the SysV and POSIX IPC system calls instead (`msg*`, `sem*`, `shm*`, `mq_*`), which the daemon never makes, so it has no IPC on Debian 11, 12 and 13 alike. Re-running the install commands of [`deploy/README.md`](deploy/README.md) installs the new unit.

### Tests

- `make test-systemd` fails when systemd reports any unknown or unparsable setting in the unit (a `lan-tracker.service:<line>:` line in the journal) on Debian 11, 12 or 13.

## v0.2.1

**Date:** 2026-10-17
**Previous release:** v0.2.0

### Summary

Patch release: the DHCP server record now keeps the **domain name** and **domain search list** a server hands out, next to its router, DNS servers and subnet mask. No change to the configuration or the schema.

### Added

- `dhcp_servers.config_json` and the API's `config` hold `domain` (option 15) and `search` (option 119, comma-separated) as last offered or assigned. An option a reply leaves out keeps its last value, so rows recorded by an earlier version pick the domain up at the server's next offer or ACK.
- `lan-tracker dhcp servers` has a `DOMAIN` column between `MASK` and `LAST SEEN`; `-o json` shows the search list too.

### Changed

- **CSV and table output of `dhcp servers` gain a column** (`DOMAIN`, before `LAST SEEN`). Scripts that read its columns by position need updating; JSON output only gains keys.

### Tests

- `make check`, with the server configuration test, the CLI output tests and the user guide's DHCP output covering the new keys.

## v0.2.0

**Date:** 2026-10-17
**Previous release:** v0.1.0

### Summary

Minor release: the v0.1.0 feature set is **frozen and documented**, with a **user guide** built from real sessions, an **end-user README** with a one-step Linux installation of the latest release, and new **chaos and parallel test suites** that found and fixed two bugs. Everything not implemented is in one place, [`docs/ROADMAP.md`](docs/ROADMAP.md). No change to the configuration, the schema or the CLI and API contracts.

### Fixed

- **A probe waiting for its target's spacing held a concurrency slot.** A pass re-probing hosts it had reached seconds earlier (an interface switched off and on by a reload, or repeated operator scans of the same hosts) could fill every slot with 5-second waits and starve probing on every other interface. The spacing is now waited out before a slot is taken; `scan plan` simulates the same order.
- **`neighbor.resync_interval` was reported as applied by `config reload`**, but the neighbour collector only reads it at start-up. It is now listed as needing a restart.

### Added

- [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md): finding devices, who held an address when, reconstructing an incident, DHCP servers, exchanges and leases, TCP endpoints, names, descriptions, the journal (`journalctl` by event type, MAC, priority and field), active discovery with `scan plan`, `scan run` and the kill switch, offline mode, scripting and the database. All output comes from the daemon replaying a simulated day at a plant, from the unit running under systemd, and from a Docker test network.
- **README for end users**, with a copy-paste installation that downloads, verifies and installs the latest release on `amd64` or `arm64`; [`deploy/README.md`](deploy/README.md) has the same download step.
- [`docs/ROADMAP.md`](docs/ROADMAP.md): field validation still open, deferred features, known gaps and limitations, and open questions; `docs/STATUS.md` and `docs/IMPLEMENTATION_PLAN.md` are folded into it and removed.
- **Chaos suite** (`make chaos`; short runs in `make check`): seeded random observation streams with late observations, long silences and restarts, checking the correlator's invariants against the database and its own state; and the whole daemon on the real clock under capture floods, malformed frames, collector and link failures, erratic probe answers, operator reads and writes, scans, reloads and the kill switch, checking a clean exit, no failed store write, a sound database, every one-second window within the budgets, nothing sent while the kill switch is set and no goroutine left behind.
- **Parallel suite**: six daemons replaying the same capture at once record identical histories; one daemon serving many concurrent operators applies every write, runs one scan at a time and answers every reader online and offline.

### Changed

- Every document checked against the code: defaults, flags, outputs, exit codes, API fields and metrics, the reload rules and the test harnesses now match what v0.2.0 does.

### Tests

- `make check`: coverage of `internal/` above 90%, race detector on, chaos and parallel suites included.
- `make chaos` (90 s of daemon chaos, 50 correlator seeds of 2,000 steps), `make fuzz`, `make test-net`, `make test-systemd`, `make soak`.
- Planted faults are caught: a conflict flag not set on the earlier holder fails the correlator chaos test at once, and budgets three times too high fail the daemon chaos test's budget check.

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
