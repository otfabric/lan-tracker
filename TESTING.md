# Testing LAN Tracker

Every Go command runs in the Linux dev container through `make`; never run `go test` directly on a macOS host (AGENTS.md rule 2). The only prerequisites are Docker, make and git. No hardware is needed for the automated tiers: the tests bring their own networks (Docker) and systemd (privileged containers).

[`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) §9 describes what each suite checks, and [`AGENTS.md`](AGENTS.md) §8 says what to run for which change. This file says how to run them.

## Tiers

| Tier | Command | Where | In CI |
| --- | --- | --- | --- |
| Unit, golden, crash, short chaos and parallel, with race | `make test` (`PKGS=...` to narrow) | `internal/`, `test/golden`, `test/crash` | yes, in `make check` |
| Coverage gate | `make coverage` | `build/coverage.sh` | yes, in `make check` |
| Per-package coverage floor | `make coverage-pkg-check` | `coverage.out` of `make coverage` | yes, in `make check` |
| Fuzz | `make fuzz` (`FUZZTIME` each, default 30s) | every `Fuzz*` target | yes, `FUZZTIME=30s` |
| Chaos and parallel at length, with race | `make chaos` (`CHAOS_DURATION`, `CHAOS_SEEDS`, `CHAOS_STEPS`) | `internal/correlate`, `internal/daemon` | no |
| Soak: 90 simulated days of a 50-host LAN | `make soak` | `test/soak`, build tag `soak` | no |
| Network integration, `CAP_NET_RAW` only | `make test-net` | `test/net`, build tag `nettest` | yes |
| systemd unit, install, privileges | `make test-systemd` | `test/systemd` | yes |
| Check, network and systemd together | `make check-all` | | no (CI runs the three separately) |
| Boards | manual | [`docs/TEST_PLAN.md`](docs/TEST_PLAN.md), `tools/capcheck` | no |

`make check` also runs fmt-check, tidy-check, vet, lint and vuln. CI (`.github/workflows/ci.yml`) runs `make mod-verify check`, `make fuzz FUZZTIME=30s`, `make test-net`, `make test-systemd` and `make package`.

## Make targets

```bash
make              # list all targets (make help)
make check        # fast gate: fmt-check, tidy-check, vet, lint, vuln, tests with race + coverage >= 90%
make check-all    # check + test-net + test-systemd
make build        # bin/lan-tracker for the Docker host's architecture
make release      # static linux/amd64 + linux/arm64 into dist/ with checksums
make package      # release tarballs per target into dist/release/ (VERSION=vX.Y.Z)
make test         # unit and golden tests with -race (PKGS=... to narrow)
make coverage     # tests with coverage of internal/ (coverage.out), fails below 90%; make cover opens the report
make coverage-pkg-check # coverage, then fails when a package is below COVER_PKG_MIN
make fuzz         # every Fuzz* target, FUZZTIME each (default 30s)
make chaos        # chaos and parallel suites at length with race (CHAOS_DURATION, CHAOS_SEEDS, CHAOS_STEPS; LT_CHAOS_SEED replays)
make soak         # 90 simulated days of a 50-host LAN: database size, memory, CPU (about 6 minutes)
make test-net     # network integration tests in Docker (CAP_NET_RAW only)
make test-systemd # daemon under systemd in containers (Debian 11, 12, 13) with the deploy/ unit
make run-dev      # daemon in the dev container on deploy/config.dev.yaml (replay)
make tools        # tools/capcheck for linux/amd64 + linux/arm64 (privilege check for boards)
make oui          # regenerate the IEEE OUI registry and the embedded table (each release)
make fixtures     # regenerate the synthetic pcap fixtures in test/fixtures
make fmt / tidy   # fix formatting / go.mod
make shell        # shell in the dev container
```

## Prerequisites and skips

- `test/net` tests need the environment that `test/net/run.sh` sets (`LT_TEST_*`). Without it they skip: run them with `make test-net`, which builds the binaries first (`build/test-bins.sh`).
- The `test/net` run needs IPv6 from Docker for the NDP checks; without it they are skipped.
- `LT_REQUIRE_NET=1` turns those skips into failures: a missing `LT_TEST_*` variable, no IPv6 network or no test binaries. CI's `docker` job sets it.
- `make test-net` and `make test-systemd` need a Docker daemon that can run the containers those scripts start.
- Some `internal/platform` tests skip when the environment lacks `SO_BINDTODEVICE`, a ping socket or an IPv4 subnet. `LT_REQUIRE_KERNEL=1` turns those skips into failures; CI's `check` job sets it.
- Other skips are not prerequisites and have no switch: tests of the unprivileged path skip with `CAP_NET_RAW` or as root, `TestICMPRawSocket` runs only in the pass of `test/net/run.sh` that disables ping sockets, and `test/crash` skips under `-short`.

## Coverage

The gate is coverage of `internal/` from every test package (`-coverpkg`), at least 90% (`COVER_MIN`). `build/coverage.sh` does not count `tools/` (exercised end to end by `test-net` and `test-systemd`), `cmd/` (`main`) or the OUI generator. `make cover` opens the report.

`make coverage-pkg-check` (in `make check`) reads the same `coverage.out` and fails when a package of `internal/` is below `COVER_PKG_MIN`, 75%. The floor is set by `internal/platform`: its AF_PACKET, netlink and raw socket paths need `CAP_NET_RAW`, so they run in `make test-net`, which is not counted. Raise a package with tests rather than lowering the floor. Percentages from this profile are per package; the ones `go test` prints with `-coverpkg` are relative to all of `internal/`, which is why `build/coverage.sh` hides them.

## Images

Every image the suites start is pinned by digest, as a manifest list so amd64 and arm64 both work: `build/dev.Dockerfile`, `test/net/run.sh` (`RUNNER_IMAGE`, `HOST_IMAGE`) and `test/systemd/run.sh` (`base_image`, with the default in `test/systemd/Dockerfile`). To move a pin, take the digest from `docker buildx imagetools inspect <image>`. The Go toolchain of the dev image moves with its pin, so a `make vuln` finding in the standard library is fixed by moving that pin.

## Replaying a failure

- Chaos: `make chaos LT_CHAOS_SEED=<seed>` replays one seed.
- Fuzz: `go test` writes a failing input to `testdata/fuzz/<target>/` in the package. Keep that file as a regression, fix the code, and run `make test`, which replays it.

## Fixtures

`test/fixtures/*.pcapng` come from `test/fixtures/fixtures.go`: edit the Go definition, run `make fixtures`, then `make check`. `TestFixturesCurrent` keeps them in step.
