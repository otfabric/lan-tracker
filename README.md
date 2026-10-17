# lan-tracker — Connectivity History for OT Networks: Which MAC Held Which IP, Names, DHCP Leases and TCP Reachability

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI Status](https://github.com/otfabric/lan-tracker/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/lan-tracker/actions/workflows/ci.yml)
[![Code Coverage](https://codecov.io/gh/otfabric/lan-tracker/graph/badge.svg)](https://codecov.io/gh/otfabric/lan-tracker)
[![Latest Release](https://img.shields.io/github/v/release/otfabric/lan-tracker?label=release)](https://github.com/otfabric/lan-tracker/releases)

LAN Tracker keeps an evidence-based history of which MAC addresses, IP addresses, names and TCP endpoints are visible on each local OT network. It runs as a single static Go binary (`lan-tracker`) under systemd on Linux edge devices and builds that history from passive capture, the kernel neighbour table and, where enabled, carefully rate-limited active probes.

Its purpose is field troubleshooting: answering, after the fact, *which MAC held this IP on this interface at that time, which other addresses and names it had, what DHCP told it, whether the configured TCP ports answered, and what the collector could not see*. It is a connectivity tracker, not an identification product: it does not determine equipment models, operating systems or firmware, and an operator's description on a MAC is the only statement about what a device is ([ADR 0012](docs/adr/0012-connectivity-tracker-scope.md)). It was migrated from LAN Sentinel ([`boeboe/lan-sentinel`](https://github.com/boeboe/lan-sentinel)); [`docs/MIGRATION.md`](docs/MIGRATION.md) records what was removed and what changed.

```bash
lan-tracker hosts list --interface eth1 --active
lan-tracker hosts find 192.168.1.200
lan-tracker hosts find 192.168.1.200 --interface eth1 --at "2026-10-04 10:15"
lan-tracker hosts history 00:0c:26:8e:1b:d6 --since 24h
lan-tracker services list --interface eth1 --ip 192.168.1.10
lan-tracker dhcp leases --interface eth1 --mac 00:0c:26:8e:1b:d6
lan-tracker events list --type duplicate-ip
lan-tracker watch --interface eth1
```

## How hosts are discovered

**Passive, always on.** Each monitored interface gets a receive-only packet capture that never sends a frame. A kernel filter keeps only what reveals hosts: ARP, DHCP, mDNS, DNS responses, LLDP and, with `passive.protocols.ipv6` on, NDP; other IPv4 traffic is cut to its headers to learn source addresses. DHCPv4 messages are decoded whole: the servers and relays that answer (one outside the interface's allowlist, `dhcp.servers`, raises `DHCP_SERVER_UNEXPECTED`), each client's exchanges, and a lease history of the addresses and configuration servers acknowledged (`dhcp servers`, `dhcp exchanges`, `dhcp leases`). A lease is the server's statement and never makes a client present. Names are kept as the records they came from (mDNS, DNS, DHCP hostname, LLDP), linked to a MAC only by evidence. Alongside the capture, the kernel's neighbour table (over netlink) reports each MAC and IP pair the box itself has resolved. On a switched port this sees the box's own traffic plus broadcast and multicast; on a mirror (SPAN) port with `promiscuous: true` it sees everything. The embedded IEEE OUI table gives each MAC prefix's registrant, a hint, not the equipment's maker.

**Active, off by default and per interface.** With `active.enabled` and explicit `networks` on an interface, an ARP sweep of those networks (every 15 minutes by default) finds devices that never speak on their own; it is the main active mechanism. ICMP echo and plain TCP connects to configured ports (80, 443, 8080 and 502 are the compiled suggestions) probe known hosts only and stay off until enabled; a TCP connect sends no payload, and OPEN means only that the connect completed. Optional name queries ask mDNS and the configured DNS resolvers about names and addresses already known. The limits are probe starts, not packets: by default 5 starts per second in all, ARP 5, ICMP 1, name queries 1, TCP 1 connect per second (at most 2 at once per interface and 1 per host), at most 4 probes in flight, 5 seconds between probes to one target, sweeps of at most 1,024 addresses, networks wider than /24 refused unless explicitly allowed, excludes checked before every probe, and a kill switch (`lan-tracker active disable`) that stops all probing at once. There is no SYN scanning, port sweeping, UDP scanning or IPv6 sweeping. `scan plan` previews a scan and `scan run` runs one on demand.

Every source only emits observations. A single correlator turns them into hosts (one per MAC per interface), address and name records with first and last seen, TCP endpoint results per address and port, and events such as `IP_ADDED` or `DUPLICATE_IP_DETECTED`. Evidence stays typed: a frame from a MAC, an address confirmed at it and an endpoint that answered refresh presence; a DHCP ACK, a DNS answer, a timeout or silence never do.

## Status

Built and verified in Docker and replay; board audits and pilot runs follow [`docs/TEST_PLAN.md`](docs/TEST_PLAN.md). Current state: [`docs/STATUS.md`](docs/STATUS.md) and [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md).

## Releases

Each release on the [Releases page](https://github.com/otfabric/lan-tracker/releases) has one tarball per target, `lan-tracker-vX.Y.Z-linux-amd64.tar.gz` and `lan-tracker-vX.Y.Z-linux-arm64.tar.gz`, each with the static binary, `capcheck`, the reference systemd unit and default config and the licence, and `SHA256SUMS` for the tarballs. What changed in each release: [RELEASE.md](RELEASE.md). A release is cut from `main` with the **release** workflow (Actions → release → Run workflow, choose a patch, minor or major bump). LAN Sentinel's tags v0.0.1 to v0.0.8 are kept and it published v0.0.9 elsewhere, so the first LAN Tracker release is a minor bump, v0.1.0. Installing, upgrading and migrating from LAN Sentinel (`config convert` translates its configuration): [`deploy/README.md`](deploy/README.md).

## Documentation

- [Requirements](docs/REQUIREMENTS.md)
- [Architecture](docs/ARCHITECTURE.md)
- [Data model](docs/DATA_MODEL.md)
- [CLI](docs/CLI.md)
- [Local API and metrics](docs/API.md)
- [Implementation plan](docs/IMPLEMENTATION_PLAN.md)
- [Hardware test plan](docs/TEST_PLAN.md)
- [Status](docs/STATUS.md) and [architecture decision records](docs/adr/README.md)
- [Migration from LAN Sentinel](docs/MIGRATION.md)
- [Deployment and releases](deploy/README.md)
- [Release notes](RELEASE.md), [contributing](CONTRIBUTING.md) and the [security policy](SECURITY.md)
- For coding agents: [AGENTS.md](AGENTS.md) and the [repository map](docs/AI_REPO_MAP.md)

## Build and test

```bash
make             # list all targets
make check       # format, tidy, vet, lint, vuln, tests with coverage
make release     # dist/lan-tracker-linux-{amd64,arm64}, SHA256SUMS
make package     # dist/release/lan-tracker-<version>-linux-{amd64,arm64}.tar.gz, SHA256SUMS
make test-net    # network integration tests in Docker
make test-systemd # the daemon under systemd in a container
make soak         # 90 simulated days of a 50-host LAN: database size, memory, CPU
```

Binaries are static (`CGO_ENABLED=0`) and need no runtime dependencies; minimum kernel 5.10. The code base is Linux only. Every make target that runs Go runs in a Linux dev container (`build/dev.Dockerfile`), so the only requirements on a development machine (Linux or macOS) are Docker, make and git. On macOS, point your editor's gopls at Linux (`GOOS=linux`). The reference systemd unit and default config are in `deploy/`.

## Runtime layout

| Item | Path |
| --- | --- |
| Binary | `/usr/local/bin/lan-tracker` |
| Config | `/etc/lan-tracker/config.yaml` |
| Database | `/data/lan-tracker/hosts.db` |
| API socket | `/run/lan-tracker/api.sock` |
| Service | `lan-tracker.service`, root with `CAP_NET_RAW` only; CLI commands need `sudo` |

## License

[MIT](LICENSE). The embedded IEEE OUI registry (`data/oui/`) is public IEEE Registration Authority data.
