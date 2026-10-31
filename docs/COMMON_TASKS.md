# LAN Tracker — Common tasks

The recipes for recurring changes. The rules they rely on are in [`AGENTS.md`](../AGENTS.md); rule numbers below refer to its §4.

## Recipes

Which files each task touches: [`AI_REPO_MAP.md`](AI_REPO_MAP.md) (Common tasks: files to touch).

**Add or change a passive decoder**

1. Read `ARCHITECTURE.md` §3 (Passive capture) and `DATA_MODEL.md` §2. Decide the MAC evidence and the `meta` keys first.
2. Write it in `internal/collect/capture/decoders` as a pure function of the frame; it never logs.
3. If it needs frames the BPF filter drops, extend `capture/filter.go` and its tests.
4. Add table tests on frames built in `test/frames`, plus a fuzz target in `decoders/fuzz_test.go`.
5. A new protocol needs a `passive.protocols` key (config, validation, docs).
6. If the synthetic site capture should show it, extend `test/fixtures/fixtures.go`, run `make fixtures`, and update the capture scenario expectations in `test/golden`.

**Add a CLI command**

1. Specify it in `docs/CLI.md` first: syntax, output, exit codes.
2. Reads: add the query to `store.Reader`, an endpoint in `internal/api` with a client method, and wire the CLI `backend` so it works online and `--offline`.
3. Writes: go through the daemon (an operator endpoint, `api.Control`, a `Bus.PublishOperator` message handled in `internal/correlate`); offline refuses.
4. Register it in `internal/cli/root.go`. Test all output formats, exit codes and the offline path. Update `docs/API.md`.

**Change the schema**

1. Add the next `migrations/NNNN_description.sql`. Never edit a migration that is on `main`.
2. Update `DATA_MODEL.md` §4 (the heading lists every migration) and the models and queries in `internal/store`.
3. Changing the event types means rebuilding `events`, because a CHECK constraint cannot be altered:
   - create the new table as `events_rebuilt` with the new CHECK (`events_new` is already an index name in `0001_init.sql`), copy the rows, drop `events` and rename;
   - recreate **all nine** indexes on `events` from `0001_init.sql`.
4. Then update `internal/events` and `DATA_MODEL.md` §7. Add the CLI type name in `CLI.md` (Event type names) if users filter on it.
5. Tests: `TestCatalogueMatchesSchema`, the migration tests in `internal/store`, a database at the previous migration upgrading.

**Add an event type**

1. Read the schema steps above.
2. Emit it from `internal/correlate` only, with its evidence snapshot.
3. Give it a journald priority (`ARCHITECTURE.md` §7).
4. Events are state transitions: never one per observation.

**Add a config key**

1. Add it to `internal/config` (`config.go`, `defaults.go`, `validate.go`) with strict validation.
2. Decide whether it reloads. If it does not, it must show up in the reload answer as needing a restart (`config.Diff` in `summary.go`, the daemon's `reload`).
3. Update `ARCHITECTURE.md` §6, `internal/config/testdata/architecture-example.yaml`, and `deploy/config.yaml` if operators should see it. `TestDeployConfigs` keeps the deploy configs valid.

**Fix or extend an existing active probe engine** (ARP, ICMP, TCP, the DNS and mDNS name queries)

1. Every send goes through `probe.Budget` (policy, kill switch, budgets, spacing) and the platform `Transmitter`.
2. Engines only emit observations.
3. Keep `scan plan` (`probe.Compute`) consistent with what `scan run` sends.
4. Run `make check-all`: `TestActiveDiscovery` measures budgets on the wire.
5. **A new probe type, or any probe that sends a payload the contract does not already name**, changes the safety contract (rules 7 and 11, ADR 0011). Identification probes are out of scope: do not add one. Anything else needs a recorded decision before any code, interface or stub.

## Keeping docs in sync

| Change | Update in the same change |
| --- | --- |
| CLI syntax or output | `CLI.md`; `README.md` examples if affected |
| API endpoint or metric | `API.md`; `ARCHITECTURE.md` §3 (Metrics) for a metric |
| Schema or migration | `DATA_MODEL.md` §4 (heading and tables) |
| Event type | `DATA_MODEL.md` §7, `CLI.md` (Event type names) |
| Config key or reload behaviour | `ARCHITECTURE.md` §6, the example config, `deploy/config.yaml` |
| Unit, install or privileges | `ARCHITECTURE.md` §8, `deploy/README.md`, `tools/capcheck/README.md` |
| A decision | `REQUIREMENTS.md` §5 (with the date); an ADR if foundational |
| A roadmap item built, or work deferred | `ROADMAP.md` (remove the item, or add it); the specifications describe only what is built |
| Operator-visible behaviour | `USER_GUIDE.md` |
| New package or moved responsibility | `ARCHITECTURE.md` §4, `docs/AI_REPO_MAP.md` |

Style used throughout the docs:

- British English (behaviour, neighbour).
- Dates written as "7 Oct 2026".
- Plain sentences; tables for reference material.
- Requirement ids `FR-<AREA>-n` / `NFR-<AREA>-n`. Never reuse or renumber an id; check with `grep` before you add one.
