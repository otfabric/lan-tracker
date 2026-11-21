# ADR 0013: A per-interface host cap, and timers independent of the host count

- Status: Accepted
- Date: 21 Nov 2026
- Specified in: `REQUIREMENTS.md` §5 (Host cap); `DATA_MODEL.md` §4, §5.6, §5.9, §7, §9; `ARCHITECTURE.md` §3 (Correlator), §6

## Context

Every distinct unicast source MAC on a captured interface became a host that the correlator held in memory for good and that was loaded again at every start. Each observation that moved time forward walked every host to evaluate presence and binding expiry, allocating and sorting a slice of bindings per host: with 8,000 hosts about 1 ms and 16,000 allocations per observation, against about 20 when time did not move. A segment that shows many distinct source MACs, such as a misbehaving device or a MAC table overflow, therefore grew the correlator's current state and its per-observation cost without bound, across restarts. The clients' open DHCP leases, keyed by client hardware address, had no bound either.

## Decision

- **Timers from a deadline queue.** Each host carries the earliest moment a presence threshold or a binding expiry may change it, in a min-heap. Advancing time evaluates only the hosts that are due, in creation order, with the same rules and the same event stamps as before; a host is recomputed when its evidence changes or a reload changes the thresholds. The cost of an observation no longer grows with the number of hosts.
- **A host cap per interface.** `identity.max_hosts_per_interface` (default 4096, at least 1, reloads) caps the hosts the correlator holds per interface. A MAC that is not a host on a full interface evicts the host on that interface that has been MISSING the longest. When none is missing, the observation is dropped and counted.
- **Eviction keeps the history.** An eviction closes the host's open bindings (`IP_REMOVED`, cause `host_cap`), ends its open name records and sets `hosts.evicted_at`; `HOST_EVICTED` records it. The row and everything that refers to it stay; nothing is deleted.
- **The same MAC is the same host.** An evicted MAC seen again on that interface is read back and held again under its `host_id`. A host stays one MAC per interface, never merged or split (ADR 0005).
- **Leases.** The same cap bounds each interface's open DHCP leases; a new client's first lease on a full interface ends the lease with the oldest ACK (`evicted`).
- **Bounded signals.** `HOST_CAP_REACHED` is emitted when an interface starts refusing and again only after it has admitted a new host since; `HOST_EVICTED` is one event per eviction, no more often than the `HOST_DISCOVERED` that causes it. Three counters labelled by interface only count evictions, refused observations and evicted leases.

## Consequences

- "Current state is never pruned" has one documented exception: the host cap ends bindings, name records and leases, and marks hosts evicted. No row is deleted, so the database still grows with history, which retention bounds as before.
- While an interface is full and no host is missing, a new device on it is not tracked until a host has been missing for `presence.stale` (24 h by default). The cap is an upper bound for unusual segments; normal OT segments stay far below it.
- An evicted MAC that returns costs one database read through the read-only reader, which sees what the writer has committed and does not make it commit; a host evicted (or described) since the last commit is read from a small in-memory list instead, kept until that commit. So batching (NFR-PERF-2) holds however many evicted MACs return. Only an interface that has evicted hosts pays the read, and only for MACs it does not hold. The row is read before anything is evicted for the MAC, so a failed read evicts nothing.
- Reads (`hosts list`, `hosts show`, the API) show an evicted host as a MISSING host with its full history.
- Migration 0002 adds `hosts.evicted_at` and rebuilds `events` for the two new types.

## Ruled out

- Deleting an evicted host's rows: its address and event history would go with it, and the foreign keys hold the history to the host.
- A new host for a returning MAC: it would split one MAC on one interface into two hosts.
- A cap without fixing the per-observation cost, or the reverse: either alone leaves one of the two unbounded.
