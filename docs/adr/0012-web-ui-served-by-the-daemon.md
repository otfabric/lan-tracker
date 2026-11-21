# ADR 0012: An opt-in web UI, served by the daemon on loopback

- Status: Accepted
- Date: 21 Nov 2026
- Specified in: `REQUIREMENTS.md` §2.9 (FR-UI-1 to FR-UI-3), §5 (Web UI, 21 Nov 2026); `API.md` (Web UI); `ARCHITECTURE.md` §6; `CLI.md` (`ui url`); `USER_GUIDE.md` §16; AGENTS.md rule 11
- Supersedes: the "A UI" item of [0011](0011-connectivity-tracker-scope.md) (Ruled out)

## Context

The CLI answers every question the tracker can answer, but a site engineer asking "what changed on this network this week?" has to run several commands and assemble the picture in their head. The maintainer asked (21 Nov 2026) for a graphical view in the style of OT Fabric's explorers: charts of activity, presence, DHCP outcomes and TCP reachability, with the event timeline and a device's history one click away. ADR 0011 had ruled out a UI; ADR 0001 (one static binary), rule 2 (Linux only) and rule 10 (least privilege) still hold.

## Decision

- **The daemon serves the UI, off by default.** `ui.enabled: true` starts a second loopback listener (`ui.listen`, default `127.0.0.1:9735`) that serves a web build embedded in the binary (`frontend/`, React, built by `make frontend` in a pinned Node container) and a token-protected copy of the read API. There is no desktop shell and no extra binary; the build stays static and cgo-free.
- **Loopback only, with a token.** The listener refuses a non-loopback address in the configuration and a non-loopback `Host` header (DNS rebinding). Its API needs a bearer token, generated once into `ui.token` next to the database (mode 0600). `lan-tracker ui url` prints the URL with the token in the fragment; the page takes it from the fragment, keeps it in memory and removes it from the address bar. From another machine the operator forwards the port over SSH.
- **On the data the tracker already has.** The UI reads the same endpoints as the CLI, plus `/v1/activity` (counts per bucket for the charts, a `store.Reader` query). Nothing is collected for it; nothing leaves the box.
- **One write: host descriptions.** The listener accepts `POST /v1/hosts/{id}/description` and nothing else that changes state. The change travels through the daemon like any operator change (ADR 0009), recorded with the actor `ui`. The kill switch, scans and `config reload` stay on the socket and the CLI.

## Consequences

- `ui` is a restart-only key. The token survives restarts; deleting `ui.token` and restarting the daemon revokes every URL handed out.
- A binary built without `make frontend` serves a placeholder page; release builds refuse to build without the UI (`build/release.sh`).
- `make check` also typechecks and tests the frontend, in the Node container.

## Ruled out

- Listening on anything but loopback, or serving the UI without the token.
- Operator actions other than descriptions from the UI (kill switch, scans, reload).
- A syslog receiver or any other new collector for the UI's sake (a separate decision; `ROADMAP.md`).
- A Wails desktop shell for now (deferred, `ROADMAP.md`).
