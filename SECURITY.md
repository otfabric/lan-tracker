# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `lan-tracker`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please email **security@otfabric.io**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/lan-tracker/security) tab → **Advisories** → **Report a vulnerability**), with:

- A description of the vulnerability
- Steps to reproduce the issue
- The affected version(s) (`lan-tracker version` output)
- Any potential impact you've identified

We will acknowledge your report within 48 hours and aim to provide a fix or mitigation within 7 days for critical issues.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | Best effort |

## Security Considerations

### What the Daemon Sends

`lan-tracker` is passive by default: each interface gets a receive-only capture and the kernel's neighbour table is read; nothing is sent. Active discovery is off until an interface sets `active.enabled` with explicit `networks`, and then sends only:

- ARP requests to the configured networks
- ICMP echo requests and plain TCP connects (no payload, closed at once) to known hosts, when enabled
- One-shot mDNS queries and unicast DNS queries to the configured resolvers about names and addresses already known, when `active.names` is enabled

Every send takes one start from a shared budget (5 starts per second in all by default), with per-target spacing, excludes and a kill switch (`lan-tracker active disable`, or `LAN_TRACKER_ACTIVE_DISABLED=1`). There is no SYN scanning, UDP port scanning, banner reading or device identification ([ADR 0011](docs/adr/0011-connectivity-tracker-scope.md)). Some OT equipment still reacts badly to unexpected traffic: review a site's devices and preview with `lan-tracker scan plan` before enabling active discovery, as [`deploy/README.md`](deploy/README.md) describes.

### Privileges

- The reference unit runs the daemon as root with `CapabilityBoundingSet=CAP_NET_RAW`, so it holds that capability alone, with a read-only view of the system (`systemd-analyze security` exposure at most 2.5, checked by `make test-systemd`)
- The daemon also works as an unprivileged user with only `CAP_NET_RAW`; it never needs `CAP_NET_ADMIN`
- Check a board with the `capcheck` tool shipped in the release tarball

### Local API and Data

- The API listens on a Unix socket (`/run/lan-tracker/api.sock`, mode 0660, owned by root); operator actions (kill switch, scans, host descriptions, configuration reload) go through it. Only grant access to people who may operate the tracker
- An optional loopback listener (`api.listen`) serves the read endpoints and Prometheus metrics to anyone on the box, never the operator endpoints, and refuses requests whose `Host` is not a loopback name; do not forward it beyond the host
- The database (`/data/lan-tracker/hosts.db`) holds MAC and IP addresses, names, DHCP configuration and TCP reachability of the site's equipment: treat it, and any copy or export, as sensitive operational data
- No data leaves the box; there is no remote API or telemetry

### Verifying Downloads

Release tarballs are published with SHA-256 checksums, and releases are reproducible (`make package VERSION=vX.Y.Z` on the tagged commit gives the same checksums). Verify them as shown in [`deploy/README.md`](deploy/README.md#install) before installing.
