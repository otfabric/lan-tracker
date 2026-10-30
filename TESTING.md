# Testing LAN Tracker

Every Go command runs in the Linux dev container through `make`; never run `go test` directly on a macOS host (AGENTS.md rule 2). The only prerequisites are Docker, make and git. No hardware is needed for the automated tiers: the tests bring their own networks (Docker) and systemd (privileged containers).

[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) §9 describes what each suite checks, and [`AGENTS.md`](AGENTS.md) §8 says what to run for which change. This file says how to run them.

## Tiers

| Tier | Command | Where | In CI |
| --- | --- | --- | --- |
| Unit, golden, crash, short chaos and parallel, with race | `make test` (`PKGS=...` to narrow) | `internal/`, `test/golden`, `test/crash` | yes, in `make check` |
| Coverage gate | `make coverage` | `build/coverage.sh` | yes, in `make check` |
| Fuzz | `make fuzz` (`FUZZTIME` each, default 30s) | every `Fuzz*` target | yes, `FUZZTIME=30s` |
| Chaos and parallel at length, with race | `make chaos` (`CHAOS_DURATION`, `CHAOS_SEEDS`, `CHAOS_STEPS`) | `internal/correlate`, `internal/daemon` | no |
| Soak: 90 simulated days of a 50-host LAN | `make soak` | `test/soak`, build tag `soak` | no |
| Network integration, `CAP_NET_RAW` only | `make test-net` | `test/net`, build tag `nettest` | yes |
| systemd unit, install, privileges | `make test-systemd` | `test/systemd` | yes |
| Check, network and systemd together | `make check-all` | | no (CI runs the three separately) |
| Boards | manual | [`docs/TEST_PLAN.md`](docs/TEST_PLAN.md), `tools/capcheck` | no |

`make check` also runs fmt-check, tidy-check, vet, lint and vuln. CI (`.github/workflows/ci.yml`) runs `make mod-verify check`, `make fuzz FUZZTIME=30s`, `make test-net`, `make test-systemd` and `make package`.

## Prerequisites and skips

- `test/net` tests need the environment that `test/net/run.sh` sets (`LT_TEST_*`). Without it they skip: run them with `make test-net`, which builds the binaries first (`build/test-bins.sh`).
- The `test/net` run needs IPv6 from Docker for the NDP checks; without it they are skipped.
- `make test-net` and `make test-systemd` need a Docker daemon that can run the containers those scripts start.
- Some `internal/platform` tests skip when the environment lacks a privilege or an IPv4 subnet, or already has `CAP_NET_RAW`. Their kernel paths are exercised by `make test-net`.
- There is no `*_REQUIRE_*` switch, so a missing prerequisite skips rather than fails. CI's `docker` job runs the real suites.

## Coverage

The gate is coverage of `internal/` from every test package (`-coverpkg`), at least 90% (`COVER_MIN`). `build/coverage.sh` does not count `tools/` (exercised end to end by `test-net` and `test-systemd`), `cmd/` (`main`) or the OUI generator. `make cover` opens the report.

## Replaying a failure

- Chaos: `make chaos LT_CHAOS_SEED=<seed>` replays one seed.
- Fuzz: `go test` writes a failing input to `testdata/fuzz/<target>/` in the package. Keep that file as a regression, fix the code, and run `make test`, which replays it.

## Fixtures

`test/fixtures/*.pcapng` come from `test/fixtures/fixtures.go`: edit the Go definition, run `make fixtures`, then `make check`. `TestFixturesCurrent` keeps them in step.
