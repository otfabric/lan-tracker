# Deployment

Reference files for running LAN Tracker under systemd (docs/ARCHITECTURE.md §8). There are no packages. Each release on the [Releases page](https://github.com/otfabric/lan-tracker/releases) has one tarball per target and `SHA256SUMS` for both:

| Asset | Contents |
| --- | --- |
| `lan-tracker-vX.Y.Z-linux-amd64.tar.gz`, `lan-tracker-vX.Y.Z-linux-arm64.tar.gz` | a directory of the same name with `lan-tracker` (the static binary), `capcheck` (the privilege check for the board audit), `lan-tracker.service`, `config.yaml` and `config.active-example.yaml` (the reference files in this directory), this `README.md`, the MIT `LICENSE`, and `SHA256SUMS` for the two binaries |
| `SHA256SUMS` | the checksums of the two tarballs |

Releases are reproducible: `make package VERSION=vX.Y.Z` on the tagged commit gives the same checksums. `config.dev.yaml` is for local development (`make run-dev`).

Targets: Debian 11 (systemd 247), 12 (252) and 13 (257), kernel 5.10 or newer; `make test-systemd` runs this unit on each. On Debian 11 the unit runs without `PrivateIPC=` (not in systemd 247; the journal shows a warning). Debian 11's regular security support ended in August 2026.

## Install

### 1. Verify and unpack

Download the tarball for the board (`linux-arm64` for a RevPi or Raspberry Pi, `linux-amd64` for an x86 edge box) and `SHA256SUMS`, then:

```bash
sha256sum --check --ignore-missing SHA256SUMS
tar -xzf lan-tracker-vX.Y.Z-linux-arm64.tar.gz && cd lan-tracker-vX.Y.Z-linux-arm64
```

### 2. Install

From the unpacked directory, as a user with `sudo`. The daemon runs as root, limited by the unit to `CAP_NET_RAW` and a read-only view of the system; it needs no user, group or extra directories beyond these. The same commands upgrade an installed release (see Upgrade and rollback): they replace the binary and the unit, keep an existing `/etc/lan-tracker/config.yaml`, and restart the daemon, which `enable --now` would not do. `make test-systemd` runs exactly this block on Debian 11, 12 and 13, then again over the running daemon.

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

On Debian 11 `daemon-reload` logs `Unknown key name 'PrivateIPC'`: systemd 247 predates the setting, and the unit runs without it.

Edit `/etc/lan-tracker/config.yaml` for the site (interfaces, and active discovery only after review, see below), check it with `sudo lan-tracker config validate`, then apply it with `sudo lan-tracker config reload`, which prints what took effect and what needs `sudo systemctl restart lan-tracker` (new interfaces, passive capture, storage, API, metrics, log format). The API socket and the database are root's (mode 0660 and 0640): run `lan-tracker` commands with `sudo`.

Then check:

| Check | Command | Expect |
| --- | --- | --- |
| Healthy | `sudo lan-tracker daemon status` | `State: ok` (exit 0); every collector of a configured interface `running` |
| Clock | same | `Clock: synced` once chrony has synchronised; events written before that are marked `unsynced` (`clock_sync` in `events list -o json`) |
| Hosts appear | `sudo lan-tracker hosts list` | the devices of the site within a few minutes |
| DHCP servers | `sudo lan-tracker dhcp servers` | the site's DHCP server(s), once a client has asked; then list their identifiers in `interfaces[].dhcp.servers` (or `[]` where none should exist) and `sudo lan-tracker config reload`, so any other raises `DHCP_SERVER_UNEXPECTED`. On a switched port replies unicast to other clients are not seen: an empty list proves nothing |
| Privileges | `grep Cap /proc/$(systemctl show -p MainPID --value lan-tracker)/status` | `CapEff` and `CapBnd` `0000000000002000`: root, but with `CAP_NET_RAW` only |
| Sandbox | `systemd-analyze security lan-tracker` | exposure ≤ 2.5 (2.3 in the container tests) |
| Board audit (once per board type) | `capcheck` under the unit's sandbox, as in `tools/capcheck/README.md` | every row `PASS` or `SKIP`, including the `adjtimex` read; record it in `docs/ARCHITECTURE.md` §8 |

## Upgrade and rollback

Migrations run at start-up and only forward; an older binary refuses a newer database (`database schema version N is newer than this binary supports`). Keep a copy for rollback. From the extracted directory of the new release, stop the daemon and copy the database:

```bash
sudo systemctl stop lan-tracker        # a clean stop checkpoints and empties the WAL
sudo test ! -s /data/lan-tracker/hosts.db-wal || echo "WAL not empty: start and stop the daemon again before copying"
sudo cp -a /data/lan-tracker/hosts.db /data/lan-tracker/hosts.db.pre-$(lan-tracker version --quiet)
```

Then run the commands of step 2: they install the new binary and unit, keep the site's config and start the daemon, which migrates the database. `sudo lan-tracker daemon status` shows the running version. New settings appear in the release's `config.yaml`; compare it with the site's (`sudo diff config.yaml /etc/lan-tracker/config.yaml`).

To roll back, stop the service, put the previous binary and the copied database back, and start it. `lan-tracker --offline` refuses a database whose schema differs from the binary's until the daemon has migrated it.

## Migrating from LAN Sentinel

LAN Tracker replaces LAN Sentinel (v0.0.x) on the same board: a new binary, unit, configuration schema and paths. What changes for operators and scripts is listed in [`docs/MIGRATION.md`](../docs/MIGRATION.md); in short:

| LAN Sentinel | LAN Tracker |
| --- | --- |
| `/usr/local/bin/lan-sentinel`, `lan-sentinel.service` | `/usr/local/bin/lan-tracker`, `lan-tracker.service` |
| `/etc/lan-sentinel/config.yaml` (schema 1) | `/etc/lan-tracker/config.yaml` (`version: 2`; schema 1 is refused, `config convert` translates it) |
| `/data/lan-sentinel/hosts.db`, `/run/lan-sentinel/api.sock` | `/data/lan-tracker/hosts.db`, `/run/lan-tracker/api.sock` |
| `LAN_SENTINEL_*` environment, `lan_sentinel_*` metrics | `LAN_TRACKER_*` (a `LAN_SENTINEL_*` variable is refused, not ignored), `lan_tracker_*` |
| identification probes, `identify run`, device types | removed (ADR 0012); the rows LAN Sentinel wrote stay readable as legacy history |
| packet-token rates | probe starts per second, with conservative defaults (`config convert` does not copy the old numbers) |
| `services` per host, `IP_CHANGED`, `HOSTNAME_CHANGED`, `MAC_MOVED`, `SERVICE_OPENED`/`CLOSED`, `DHCP_CONFIG_CHANGED` | TCP endpoints per address (`services list`), `IP_ADDED`/`IP_REMOVED`, `HOSTNAME_ADDED`/`REMOVED`, `MAC_ALSO_OBSERVED`, `SERVICE_STATE_CHANGED`, per-client DHCP lease history (`dhcp leases`); the old events stay in history with their wording |

The first cutover migrates a **copy** of LAN Sentinel's database into LAN Tracker's path, so LAN Sentinel's binary, unit, configuration and database stay untouched for rollback. Only one daemon may run at a time: two would capture and probe the same network twice and write two databases. Migration itself sends nothing.

### 1. Stage, without stopping LAN Sentinel

From the unpacked LAN Tracker release (see Install, step 1):

```bash
sha256sum --check SHA256SUMS
sudo install -D -m 0755 lan-tracker /usr/local/bin/lan-tracker
lan-tracker config convert /etc/lan-sentinel/config.yaml > config.converted.yaml
```

`config convert` writes nothing: it prints the converted file and, on stderr, a report of what was removed (identification, UDP probes), what changed meaning (rates) and what to review. Active discovery is off on every interface in the result, whatever LAN Sentinel had. Review `config.converted.yaml` against the report and the release's `config.active-example.yaml`, then install and validate it:

```bash
sudo install -D -m 0640 config.converted.yaml /etc/lan-tracker/config.yaml
sudo lan-tracker config validate --config /etc/lan-tracker/config.yaml
sudo install -D -m 0644 lan-tracker.service /etc/systemd/system/lan-tracker.service
sudo systemctl daemon-reload
```

Do not start `lan-tracker` yet.

### 2. Stop LAN Sentinel and copy its database

```bash
sudo systemctl stop lan-sentinel
sudo systemctl disable lan-sentinel
sudo test ! -s /data/lan-sentinel/hosts.db-wal || echo "WAL not empty: LAN Sentinel did not stop cleanly"
sudo install -d -m 0750 /data/lan-tracker
sudo cp -a /data/lan-sentinel/hosts.db /data/lan-tracker/hosts.db
date -u +%FT%TZ | sudo tee /data/lan-tracker/migrated-from-lan-sentinel
```

A clean stop checkpoints the WAL into the database and empties it. If the WAL is not empty, start and stop LAN Sentinel again rather than copying a database without its WAL. The time written last is the cutover: LAN Sentinel's history ends there and LAN Tracker's begins.

### 3. Start LAN Tracker passive and check the history

```bash
sudo systemctl enable lan-tracker
sudo systemctl start lan-tracker
sudo lan-tracker daemon status
```

The daemon migrates the copy at start-up (migrations 0010 to 0013) and resumes from it. Active discovery stays off: the converted configuration enables none, and a kill switch LAN Sentinel persisted (`active disable`) carries over. Check that history survived:

| Check | Command | Expect |
| --- | --- | --- |
| Hosts and descriptions | `sudo lan-tracker hosts list` | the same host IDs and descriptions as `sudo lan-sentinel hosts list` showed before the stop |
| Bindings and names | `sudo lan-tracker hosts show <host>` | open addresses as before; LAN Sentinel's names as legacy records (TTL unknown); its services as legacy, without an address |
| Events | `sudo lan-tracker events list --since 30d` | LAN Sentinel's events with their original types and wording |
| DHCP | `sudo lan-tracker dhcp servers`, `sudo lan-tracker dhcp leases` | the servers; leases backfilled from LAN Sentinel's raw observations still in the database (marked backfilled, configuration unknown) |
| Kill switch | `sudo lan-tracker daemon status` | `Active:` as LAN Sentinel had it |
| Integrity | `sudo lan-tracker db check` | `ok` |

### 4. Enable active discovery

Only after the passive checks, and as for any site (Staged rollout, step 4): enable the reviewed ARP and TCP profile on the interface (`config.active-example.yaml`), `sudo lan-tracker config validate`, preview with `sudo lan-tracker scan plan`, then `sudo lan-tracker config reload`. If LAN Sentinel's kill switch carried over, clear it with `sudo lan-tracker active enable --reason "..."` once the profile is reviewed.

### Rolling back to LAN Sentinel

LAN Sentinel cannot open the migrated database (its schema is newer), and its own database was not touched. To go back:

```bash
sudo systemctl stop lan-tracker
sudo systemctl disable lan-tracker
sudo systemctl enable lan-sentinel
sudo systemctl start lan-sentinel
```

LAN Sentinel resumes from its database as it was at the cutover: what LAN Tracker observed after the cutover stays in `/data/lan-tracker/hosts.db` and is not available to LAN Sentinel. Keep LAN Sentinel's binary, unit, configuration and database until LAN Tracker has run through the site's acceptance; then remove them (`/usr/local/bin/lan-sentinel`, `/etc/systemd/system/lan-sentinel.service`, `/etc/lan-sentinel`, `/data/lan-sentinel`).

## Cutting a release

Releases are made from `main` only, by the **release** workflow (`.github/workflows/release.yml`):

1. GitHub → Actions → **release** → **Run workflow**, on branch `main`.
2. Choose the bump: `patch` (v0.0.1 → v0.0.2), `minor` (→ v0.1.0) or `major` (→ v1.0.0). The next version follows the highest `vX.Y.Z` tag (`build/next-version.sh`; pre-release tags are ignored); in a repository without tags the first release is v0.0.1 whatever the bump. This repository keeps LAN Sentinel's tags v0.0.1 to v0.0.8, and LAN Sentinel published v0.0.9 from its own repository: cut LAN Tracker's first release as `minor` (v0.1.0) so no version names two products.
3. The workflow refuses any branch but `main`, a version whose tag exists already and a version without a `## vX.Y.Z` section in [`RELEASE.md`](../RELEASE.md) (write the notes first, in the release's pull request), runs the same gates as CI on the commit (`make mod-verify check`, `make fuzz`, `make test-net`, `make test-systemd`), builds the tarballs twice and compares their checksums, checks that the binary reports the version, creates the annotated tag `vX.Y.Z` on the tested commit and publishes the GitHub Release with the two tarballs and `SHA256SUMS`, with that section of `RELEASE.md` as its notes.

Runs are serialised, so two releases never pick the same version. If a run fails after the tag was pushed (the release step), delete the tag before running it again, or the next run picks the following version.

## Staged rollout (each release, docs/IMPLEMENTATION_PLAN.md)

1. Lab rig, passive only, full soak (7 days with a real PLC, inverter and HMI).
2. Three pilot sites passive only for two weeks; compare `hosts list` with what field support knows is there.
3. 50 sites (5%) passive only, then the whole fleet passive only. Watch in Prometheus (`api.listen` and `metrics.enabled`):

   | Metric | Watch for |
   | --- | --- |
   | `lan_tracker_collector_up` | 0: a collector failed (`daemon status` names it) |
   | `lan_tracker_capture_drops_total`, `lan_tracker_bus_dropped_total` | growth: a busy mirror port; raise `passive.ring_size` |
   | `lan_tracker_db_size_bytes` | approaching `storage.retention.max_db_size` (budget: < 200 MB after 90 days on a 50-host LAN) |
   | `lan_tracker_dhcp_servers{status="unexpected"}` | above 0: a DHCP server outside the allowlist answered (`dhcp servers`, `events list --type dhcp-server-unexpected`) |
   | process CPU and RSS (`systemctl show -p CPUUsageNSec,MemoryCurrent lan-tracker`) | above 5% CPU or 50 MB on a RevPi Connect |

   A misbehaving decoder is switched off fleet-wide through `passive.protocols` and `systemctl restart lan-tracker`, without a new build (the protocols are compiled into the capture filter, so a reload does not change them).
4. Active discovery on the pilot sites, then site by site after reviewing the site's device mix:
   - set `active.enabled: true` with the site's `networks` and `exclude` (fragile devices, gateways) on the interface, and the periodic probes under `active:`; `lan-tracker config validate` shows the sweep size and duration;
   - preview with `lan-tracker scan plan --profile <profile>`, then `lan-tracker scan run --profile <profile>` while someone watches the devices;
   - `sudo lan-tracker config reload` to start the periodic probes (it lists `interfaces[N].active.enabled` as applied, and the journal says `configuration reloaded; active discovery: arp every 5m on eth0 (…)`); periodic passes are not logged, so check them with `sudo lan-tracker observations list --source arp_scan`;
   - watch `lan_tracker_probe_total` and `lan_tracker_probe_throttled_total`.

   If anything misbehaves: `lan-tracker active disable --reason "..."` stops every probe at once and stays set across restarts (`active enable` clears it). `LAN_TRACKER_ACTIVE_DISABLED=1` in the unit's environment forces it off fleet-wide.

## Everyday tasks

| Task | Command |
| --- | --- |
| Status | `systemctl status lan-tracker`, `sudo lan-tracker daemon status` |
| Reload configuration | `sudo lan-tracker config reload` (prints what took effect; `systemctl reload lan-tracker` does the same silently) |
| Events in the journal | `journalctl -u lan-tracker EVENT=ip_added` |
| Security exposure | `systemd-analyze security lan-tracker` (target ≤ 2.5) |

`lan-tracker` commands, online or `--offline`, need `sudo`: the socket and the database belong to root.
