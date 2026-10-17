# Contributing to lan-tracker

Thank you for your interest in contributing to `lan-tracker`! This document covers the guidelines for contributing. [`AGENTS.md`](AGENTS.md) holds the full set of rules (for people and AI agents alike): read its hard rules (§4) before changing code.

## Getting Started

1. Fork the repository on GitHub
2. Clone your fork locally:
   ```sh
   git clone git@github.com:<your-username>/lan-tracker.git
   cd lan-tracker
   ```
3. Create a feature branch:
   ```sh
   git checkout -b feature/my-change
   ```

## Development Setup

### Prerequisites

- Docker. Every Go command runs in the Linux dev container through `make`; the first run builds the image from [`build/dev.Dockerfile`](build/dev.Dockerfile) (Go and golangci-lint; govulncheck is pinned as a tool in `go.mod`). Do not run `go` directly on a macOS host
- No hardware: the tests bring their own networks (Docker) and systemd (privileged containers)

### Build & Test

```sh
make              # list every target
make check        # format, tidy, vet, lint, vuln, race tests, coverage of internal/ >= 90%
make check-all    # check plus the Docker network tests and the systemd tests
make test PKGS=./internal/store/...   # one package while iterating
make fuzz         # every Fuzz* target (FUZZTIME, default 30s)
make chaos        # chaos and parallel suites at length with the race detector (CHAOS_DURATION=60s by default)
make soak         # 90 simulated days of a 50-host LAN (several minutes)
make build        # bin/lan-tracker for the Docker host's architecture
```

[`AGENTS.md`](AGENTS.md) §8 says which of these to run for which change.

## Making Changes

### Code Style

- Follow standard Go conventions (`gofmt -s`, `go vet`); `make fmt` formats with the container's Go
- Write like the surrounding code: comment density, naming and idiom; comments explain why and cite the doc section or rule
- No `//nolint` and no lowered coverage threshold to get a gate green
- Keep it simple: the smallest change that meets the requirement

### Safety Contract

Changes to what the daemon sends are changes to its safety contract ([ADR 0011](docs/adr/0011-connectivity-tracker-scope.md), `AGENTS.md` rules 7 and 11). Device identification of any kind is out of scope and will not be accepted. A new probe type, or a probe sending a payload the contract does not name, needs a recorded decision before any code.

### Commit Messages

Use clear, concise commit messages:

```
component: short description

Optional longer explanation of the change, why it was made,
and any relevant context.
```

Examples:
- `correlate: close exchanges left open by a restart`
- `decoders: read option 119 from overloaded sname`
- `docs: document the staged rollout of active discovery`

### Testing

- Decoders: table tests on frames built with [`test/frames`](test/frames), and a fuzz target in `decoders/fuzz_test.go`
- Correlator: golden observation streams ([`test/golden`](test/golden)); the reconstruction scenario must always pass
- Platform backends and probes on the wire: [`test/net`](test/net) (`make test-net`), run as uid 65534 with only `CAP_NET_RAW`
- Chaos and parallel suites: extend `internal/correlate/chaos_test.go` with new observation kinds and invariants, and `internal/daemon/chaos_test.go` / `parallel_test.go` with new operator actions; a failure prints the seed (`make chaos LT_CHAOS_SEED=<seed>` replays it). Check that a planted bug makes them fail
- Assert behaviour (rows, events, output, exit codes), not just that the code ran
- Coverage of `internal/` stays at or above 90% (`make coverage`)

### Documentation

Keep the docs in sync in the same change ([`AGENTS.md`](AGENTS.md) §12): `docs/CLI.md` and `docs/API.md` for commands and endpoints, `docs/DATA_MODEL.md` for the schema and events, `docs/ARCHITECTURE.md` for configuration and the unit, and [RELEASE.md](RELEASE.md) for anything operators or scripts will notice. Docs are written in British English.

## Submitting Changes

1. Push your branch to your fork:
   ```sh
   git push origin feature/my-change
   ```
2. Open a Pull Request against the `main` branch
3. Describe what your change does and why
4. Reference any related issues

For larger changes, please open an issue first to discuss the approach.

## Reporting Issues

- Use GitHub Issues to report bugs or suggest features
- Include the output of `lan-tracker version` and `sudo lan-tracker daemon status` in bug reports
- Provide the exact command and its output; for the daemon, the relevant `journalctl -u lan-tracker` lines
- Mention the board, the Debian release and whether the port is switched or mirrored
- Report security vulnerabilities privately: see [SECURITY.md](SECURITY.md)

## Project Structure

[`AGENTS.md`](AGENTS.md) §3 maps every package, and [`docs/AI_REPO_MAP.md`](docs/AI_REPO_MAP.md) says where things live.

## License

By contributing to `lan-tracker`, you agree that your contributions will be licensed under the [MIT License](LICENSE).
