# ADR 0008: Active discovery inside a fixed OT safety envelope

- Status: Accepted
- Date: 5 Oct 2026 (v1 specification); refined 6 Oct 2026
- Amended: 16 Oct 2026 (budgets count probe starts, [ADR 0011](0011-connectivity-tracker-scope.md); name queries join the envelope)
- Specified in: `ARCHITECTURE.md` §3 (Active probes), §5; `REQUIREMENTS.md` §2.4, NFR-SAFE-1 to NFR-SAFE-5, §4, §5 (Probe targets, IPv4-first, Budget enforcement, TCP close and packets, Back-off scope, Sweep size); AGENTS.md rules 7, 11, 12

## Context

The networks are industrial. PLCs, inverters and HMIs can fault under unexpected traffic, and a fault can stop a site. About a thousand boxes may update and start probing at once. Some devices never speak unless spoken to, so passive discovery alone misses them.

## Decision

Active discovery is off by default, enabled per interface, and scoped:

- **What runs.** An ARP sweep of explicitly configured networks is the primary mechanism. ICMP echo and bare TCP connects to configured ports touch known hosts and configured targets only. Name queries (mDNS, unicast DNS) ask only about names and addresses already known, or configured DNS-SD types.
- **The budget.** Every send takes one start from a shared budget, which checks:
  - the kill switch and the policy;
  - excludes;
  - a global start rate (5 starts/s) and per-protocol start rates;
  - concurrency caps, and the TCP caps per interface and per target;
  - at least 5 s between probes to one target.

  The rate contract is probe starts per second, not packets on the wire (ADR 0011, amended 16 Oct 2026). Budgets are paced and enforced over a sliding 1.02 s window on actual send times.
- **Timing.** A start-up delay with jitter, randomised target order, and back-off after three unanswered probes.
- **Scope guards.** Prefixes wider than /24 are refused unless explicitly allowed, and a sweep covers at most `active.max_sweep_targets` addresses. `scan plan` previews exactly what `scan run` does.
- **TCP.** A plain `connect()`, closed at once with a RST, with no payload; the kernel sends at most 3 packets per connect.
- **No answer means nothing.** It is never "offline".

## Consequences

- Probe code is concentrated in `internal/probe`. Engines only emit observations and send only through the platform `Transmitter`.
- `make test-net` measures every one-second window on the wire against the start budgets and counts the kernel's own packets apart.
- The kill switch persists across restarts and stops running passes at once.

## Ruled out

- SYN scanning, generic UDP port scanning, register scanning and IPv6 sweeping. NDP solicitation is not built: v1 is IPv4-first (`ROADMAP.md`).
- Scanning discovered subnets automatically.
- UDP probes other than the DNS and mDNS name queries, and any probe that sends a payload to identify a device (Modbus, HTTP, TLS, SNMP, banners): device identification is out of scope (ADR 0011).
