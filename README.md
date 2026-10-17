# lan-tracker — Connectivity History for OT Networks: Which MAC Held Which IP, Names, DHCP Leases and TCP Reachability

[![Go Version](https://img.shields.io/badge/Go-1.26+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI Status](https://github.com/otfabric/lan-tracker/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/lan-tracker/actions/workflows/ci.yml)
[![Code Coverage](https://codecov.io/gh/otfabric/lan-tracker/graph/badge.svg)](https://codecov.io/gh/otfabric/lan-tracker)
[![Latest Release](https://img.shields.io/github/v/release/otfabric/lan-tracker?label=release)](https://github.com/otfabric/lan-tracker/releases)

**lan-tracker keeps a history of what is connected to your industrial (OT) networks**: which MAC address held which IP address on which interface and when, the names those devices announced or resolved to, what the DHCP servers told them, whether their configured TCP ports answered, and when the tracker itself could not see. It runs as one small daemon on a Linux edge device (Raspberry Pi CM4, RevPi Connect, an x86 edge box) and answers questions after the fact:

- *Which device had 192.168.110.51 yesterday at 10:15, and where did it go?*
- *Since when do two devices claim the same IP address?*
- *Which DHCP server gave this PLC its address, with which gateway and DNS servers?*
- *When did port 502 on this inverter stop answering, and when did it last answer?*
- *Was the tracker running during the incident?*

It listens passively by default and sends nothing. Optional active discovery (ARP sweeps, ping, plain TCP connects, name queries) is off until you enable it per interface for explicit networks, and stays inside a strict rate budget with a kill switch. It does **not** identify equipment: no models, firmware or operating systems, no banner grabbing; your own description on a device is the only statement about what it is.

## Install on Linux

Requirements: a systemd Linux (tested on Debian 11, 12 and 13), kernel 5.10 or newer, `amd64` or `arm64`, and `sudo`. There are no packages: a release is one tarball per architecture with the static binary, the systemd unit and a default configuration.

Download, verify and unpack the latest release:

```bash
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64 | arm64) arch=arm64 ;; *) echo "unsupported: $(uname -m)" >&2; exit 1 ;; esac
tag=$(curl -fsSLI -o /dev/null -w '%{url_effective}' https://github.com/otfabric/lan-tracker/releases/latest | sed 's|.*/||')
cd "$(mktemp -d)"
curl -fsSLO "https://github.com/otfabric/lan-tracker/releases/download/$tag/lan-tracker-$tag-linux-$arch.tar.gz"
curl -fsSLO "https://github.com/otfabric/lan-tracker/releases/download/$tag/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS
tar -xzf "lan-tracker-$tag-linux-$arch.tar.gz" && cd "lan-tracker-$tag-linux-$arch"
```

Install and start it (the same commands upgrade an existing installation and keep its configuration):

```bash
sha256sum --check SHA256SUMS
sudo install -D -m 0755 lan-tracker /usr/local/bin/lan-tracker
sudo test -e /etc/lan-tracker/config.yaml || sudo install -D -m 0640 config.yaml /etc/lan-tracker/config.yaml
sudo install -d -m 0750 /data/lan-tracker
sudo lan-tracker config validate --config /etc/lan-tracker/config.yaml
sudo install -D -m 0644 lan-tracker.service /etc/systemd/system/lan-tracker.service
sudo systemctl daemon-reload
sudo systemctl enable lan-tracker
sudo systemctl restart lan-tracker
```

Then tell it which interface faces the OT network. The default configuration watches `eth0` passively; edit `/etc/lan-tracker/config.yaml`:

```yaml
version: 1
interfaces:
  - name: eth1                               # the interface on the OT network
    passive: { enabled: true, promiscuous: false }   # promiscuous: true only on a mirror/SPAN port
    active:  { enabled: false }              # nothing is sent until you enable it
    dhcp:    { servers: [192.168.110.2] }    # optional: the expected DHCP server(s); others raise an event
```

```bash
sudo lan-tracker config validate     # checks the file, lists what each interface will do
sudo systemctl restart lan-tracker   # a new interface needs a restart; most other changes: sudo lan-tracker config reload
sudo lan-tracker daemon status       # State: OK, every collector running
```

The daemon runs as root but holds only the `CAP_NET_RAW` capability, with a read-only view of the system. Its API socket and database belong to root, so run `lan-tracker` commands with `sudo`. [`deploy/README.md`](deploy/README.md) covers the details: checks after installing, upgrades and rollback, staged rollout and enabling active discovery.

## First steps

After a few minutes on the network:

```bash
sudo lan-tracker hosts list                               # every device seen, with presence and addresses
sudo lan-tracker hosts find 192.168.110.51                # who holds this IP now
sudo lan-tracker hosts find 192.168.110.51 --at "2026-10-16 10:15"   # ... and who held it then
sudo lan-tracker hosts history 192.168.110.51 --since 7d  # every holder of the IP, in order
sudo lan-tracker hosts show 00:1b:1b:aa:bb:01             # one device: addresses, names, endpoints, DHCP leases
sudo lan-tracker hosts set 00:1b:1b:aa:bb:01 --description "Inverter, north roof"
sudo lan-tracker events list --since 24h                  # what changed: new devices, IP changes, conflicts, DHCP
sudo lan-tracker dhcp servers                             # the DHCP servers that answered, and their verdicts
sudo lan-tracker dhcp leases --open                       # the leases servers acknowledged, per client
sudo lan-tracker watch                                    # events as they happen
```

Every change is also written to the journal, one line per event:

```bash
journalctl -u lan-tracker -f                              # follow
journalctl -u lan-tracker EVENT=duplicate_ip_detected     # one event type
journalctl -u lan-tracker MAC=00:1b:1b:aa:bb:01 -o verbose  # everything about one device, with all fields
```

The [user guide](docs/USER_GUIDE.md) walks through real sessions: finding a device, reconstructing an incident, reading DHCP history, TCP endpoints, the journal, active discovery and the offline mode for when the daemon is down.

## What it watches

- **Passively, always:** ARP, DHCP, mDNS, DNS answers, LLDP and IPv4 source addresses seen on the interface (IPv6 neighbour discovery when enabled), plus the kernel's own neighbour table. On a switched port that is the box's own traffic plus broadcast and multicast; on a mirror port, everything.
- **Actively, only when enabled per interface:** ARP sweeps of the networks you list, ping and plain TCP connects (no payload) to known devices, and DNS/mDNS questions about names and addresses already seen. Every probe takes a slot from a budget (by default 5 per second in all), excluded addresses are never touched, and `sudo lan-tracker active disable --reason "..."` stops all probing at once and stays set across restarts. Preview any scan with `lan-tracker scan plan`.
- **History:** a device is one MAC on one interface; its addresses, names, DHCP leases and TCP results each have first-seen and last-seen times, and every material change is an event. Raw observations are kept 7 days, hourly summaries 90 days and events 2 years, inside a 200 MB database budget.
- **Local only:** a REST API on a Unix socket, optional Prometheus metrics on loopback, and the CLI. No data leaves the box.

## Documentation

- [User guide](docs/USER_GUIDE.md): practical examples with real output
- [Deployment](deploy/README.md): install, upgrade, rollback, rollout and active discovery
- [CLI reference](docs/CLI.md) and [local API and metrics](docs/API.md)
- [Release notes](RELEASE.md) and the [roadmap](docs/ROADMAP.md) (what is not implemented)
- [Security policy](SECURITY.md)
- For developers: [requirements](docs/REQUIREMENTS.md), [architecture](docs/ARCHITECTURE.md), [data model](docs/DATA_MODEL.md), [decision records](docs/adr/README.md), the [hardware test plan](docs/TEST_PLAN.md), [contributing](CONTRIBUTING.md), and for coding agents [AGENTS.md](AGENTS.md)

## Build from source

Everything builds and tests in a Linux dev container, so a development machine (Linux or macOS) needs only Docker, make and git:

```bash
make check     # format, lint, vulnerability check, tests with the race detector and coverage
make package   # dist/release/lan-tracker-<version>-linux-{amd64,arm64}.tar.gz
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full list of targets and test suites.

## License

[MIT](LICENSE). The embedded IEEE OUI registry (`data/oui/`) is public IEEE Registration Authority data.
