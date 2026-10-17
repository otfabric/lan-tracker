-- LAN Tracker schema (docs/DATA_MODEL.md §3–§4). Later changes are new,
-- numbered migrations; this file never changes once released.
--
-- Timestamps are UTC Unix milliseconds. Binding tables use first_seen
-- (inclusive), last_seen (last evidence, never NULL) and ended_at (NULL while
-- open, exclusive end once closed).

-- Network contexts: one per monitored interface, with its prefix history.
CREATE TABLE network_contexts (
    id         INTEGER PRIMARY KEY,
    interface  TEXT    NOT NULL UNIQUE,
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    CHECK (last_seen >= first_seen)
) STRICT;

CREATE TABLE context_prefixes (
    id         INTEGER PRIMARY KEY,
    context_id INTEGER NOT NULL REFERENCES network_contexts (id),
    prefix     TEXT    NOT NULL,
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    ended_at   INTEGER,
    CHECK (last_seen >= first_seen),
    CHECK (ended_at IS NULL OR ended_at >= first_seen)
) STRICT;

CREATE UNIQUE INDEX context_prefixes_open ON context_prefixes (context_id, prefix) WHERE ended_at IS NULL;

-- Hosts: one per (context, MAC), keyed by a UUID. oui_registrant is the MAC
-- prefix's IEEE registrant, a hint, never the equipment's maker. The three
-- evidence times are kept apart (§2); last_seen is their maximum and drives
-- presence. description is the operator's (§5.8).
CREATE TABLE hosts (
    host_id                TEXT    PRIMARY KEY,
    context_id             INTEGER NOT NULL REFERENCES network_contexts (id),
    mac                    TEXT    NOT NULL,
    oui_registrant         TEXT,
    locally_administered   INTEGER NOT NULL DEFAULT 0 CHECK (locally_administered IN (0, 1)),
    presence               TEXT    NOT NULL CHECK (presence IN ('ACTIVE', 'RECENT', 'STALE', 'MISSING')),
    preferred_name         TEXT,
    description            TEXT,
    proxy_arp              INTEGER NOT NULL DEFAULT 0 CHECK (proxy_arp IN (0, 1)),
    first_seen             INTEGER NOT NULL,
    last_seen              INTEGER NOT NULL,
    last_frame_seen        INTEGER,
    last_address_confirmed INTEGER,
    last_endpoint_answer   INTEGER,
    UNIQUE (context_id, mac),
    CHECK (last_seen >= first_seen)
) STRICT;

CREATE INDEX hosts_mac ON hosts (mac);

-- Address bindings: a host holding an IP for an interval; several hosts
-- holding one IP at once is a conflict.
CREATE TABLE addresses (
    id         INTEGER PRIMARY KEY,
    context_id INTEGER NOT NULL REFERENCES network_contexts (id),
    host_id    TEXT    NOT NULL REFERENCES hosts (host_id),
    ip         TEXT    NOT NULL,
    family     INTEGER NOT NULL CHECK (family IN (4, 6)),
    conflict   INTEGER NOT NULL DEFAULT 0 CHECK (conflict IN (0, 1)),
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    ended_at   INTEGER,
    CHECK (last_seen >= first_seen),
    CHECK (ended_at IS NULL OR ended_at >= first_seen)
) STRICT;

CREATE UNIQUE INDEX addresses_open ON addresses (host_id, ip) WHERE ended_at IS NULL;
CREATE INDEX addresses_point_in_time ON addresses (context_id, ip, first_seen);
CREATE INDEX addresses_host ON addresses (host_id, first_seen);

CREATE TABLE address_sources (
    address_id INTEGER NOT NULL REFERENCES addresses (id),
    source     TEXT    NOT NULL,
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    PRIMARY KEY (address_id, source),
    CHECK (last_seen >= first_seen)
) STRICT, WITHOUT ROWID;

-- Raw observations within their retention, and the hourly roll-ups they are
-- folded into.
CREATE TABLE observations (
    id           INTEGER PRIMARY KEY,
    ts           INTEGER NOT NULL,
    context_id   INTEGER NOT NULL,
    source       TEXT    NOT NULL,
    mac          TEXT,
    ip           TEXT,
    hostname     TEXT,
    name_type    TEXT,
    service_json TEXT,
    meta_json    TEXT,
    host_id      TEXT
) STRICT;

CREATE INDEX observations_ts ON observations (ts);
CREATE INDEX observations_host ON observations (host_id, ts);

CREATE TABLE observation_rollups (
    id         INTEGER PRIMARY KEY,
    hour       INTEGER NOT NULL,
    context_id INTEGER NOT NULL,
    host_id    TEXT,
    source     TEXT    NOT NULL,
    mac        TEXT,
    ip         TEXT,
    count      INTEGER NOT NULL CHECK (count > 0),
    first_ts   INTEGER NOT NULL,
    last_ts    INTEGER NOT NULL,
    CHECK (last_ts >= first_ts)
) STRICT;

CREATE UNIQUE INDEX observation_rollups_key ON observation_rollups (
    hour, context_id, coalesce(host_id, ''), source, coalesce(mac, ''), coalesce(ip, '')
);
CREATE INDEX observation_rollups_hour ON observation_rollups (hour);
CREATE INDEX observation_rollups_host ON observation_rollups (host_id, hour);

-- Operator scans, and state that survives restarts (the kill switch, the
-- correlator's heartbeat).
CREATE TABLE scans (
    id           INTEGER PRIMARY KEY,
    context_id   INTEGER NOT NULL REFERENCES network_contexts (id),
    kind         TEXT    NOT NULL,
    trigger      TEXT    NOT NULL CHECK (trigger IN ('scheduled', 'operator')),
    started_at   INTEGER NOT NULL,
    finished_at  INTEGER,
    targets      INTEGER NOT NULL DEFAULT 0,
    results_json TEXT    NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX scans_started ON scans (started_at);

CREATE TABLE runtime_state (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- Events: material state transitions, each self-contained with its evidence
-- (§7). The CHECK lists exactly the catalogue of internal/events.
CREATE TABLE events (
    id              INTEGER PRIMARY KEY,
    ts              INTEGER NOT NULL,
    type            TEXT    NOT NULL CHECK (type IN (
        'HOST_DISCOVERED', 'HOST_DISAPPEARED', 'HOST_REAPPEARED', 'HOST_DESCRIBED',
        'IP_ADDED', 'IP_REMOVED', 'MAC_ALSO_OBSERVED', 'PROXY_ARP_DETECTED',
        'DUPLICATE_IP_DETECTED', 'DUPLICATE_IP_RESOLVED',
        'HOSTNAME_ADDED', 'HOSTNAME_REMOVED', 'SERVICE_STATE_CHANGED',
        'SCAN_STARTED', 'SCAN_COMPLETED', 'ACTIVE_DISABLED', 'ACTIVE_ENABLED',
        'INTERFACE_UP', 'INTERFACE_DOWN', 'SUBNET_CHANGED', 'DATABASE_RECREATED', 'OBSERVATION_GAP',
        'DHCP_SERVER_DISCOVERED', 'DHCP_SERVER_UNEXPECTED', 'DHCP_SERVER_MAC_CHANGED',
        'DHCP_LEASE_ASSIGNED', 'DHCP_CLIENT_CONFIG_CHANGED', 'DHCP_LEASE_NAK', 'DHCP_LEASE_DECLINED', 'DHCP_LEASE_RELEASED'
    )),
    severity        TEXT    NOT NULL CHECK (severity IN ('info', 'notice', 'warning')),
    context_id      INTEGER REFERENCES network_contexts (id),
    host_id         TEXT    REFERENCES hosts (host_id),
    related_host_id TEXT    REFERENCES hosts (host_id),
    old_value       TEXT,
    new_value       TEXT,
    cause           TEXT    NOT NULL,
    observation_id  INTEGER,
    evidence_json   TEXT    NOT NULL DEFAULT '{}',
    clock_sync      TEXT    NOT NULL DEFAULT 'unknown' CHECK (clock_sync IN ('synced', 'unsynced', 'unknown'))
) STRICT;

CREATE INDEX events_ts ON events (ts);
CREATE INDEX events_host ON events (host_id, ts);
CREATE INDEX events_type ON events (type, ts);
CREATE INDEX events_context ON events (context_id, ts);
CREATE INDEX events_related ON events (related_host_id, ts);
CREATE INDEX events_context_type ON events (context_id, type, ts);
CREATE INDEX events_old ON events (old_value);
CREATE INDEX events_new ON events (new_value);
CREATE INDEX events_evidence_ip ON events (json_extract(evidence_json, '$.ip'));

-- TCP endpoints per (context, IP, port) and every change of their result
-- (§5.5). host_id is the sole fresh holder of the address at the result.
CREATE TABLE endpoints (
    id                   INTEGER PRIMARY KEY,
    context_id           INTEGER NOT NULL REFERENCES network_contexts (id),
    ip                   TEXT    NOT NULL,
    proto                TEXT    NOT NULL CHECK (proto IN ('tcp')),
    port                 INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    host_id              TEXT    REFERENCES hosts (host_id),
    last_result          TEXT    NOT NULL CHECK (last_result IN ('OPEN', 'REFUSED', 'TIMEOUT', 'UNREACHABLE', 'LOCAL_ERROR')),
    last_result_at       INTEGER NOT NULL,
    last_detail          TEXT,
    last_open_at         INTEGER,
    last_definite_result TEXT    CHECK (last_definite_result IN ('OPEN', 'REFUSED')),
    last_definite_at     INTEGER,
    first_seen           INTEGER NOT NULL,
    UNIQUE (context_id, ip, proto, port),
    CHECK (last_result_at >= first_seen)
) STRICT;

CREATE INDEX endpoints_host ON endpoints (host_id);

CREATE TABLE endpoint_results (
    id          INTEGER PRIMARY KEY,
    endpoint_id INTEGER NOT NULL REFERENCES endpoints (id),
    ts          INTEGER NOT NULL,
    result      TEXT    NOT NULL CHECK (result IN ('OPEN', 'REFUSED', 'TIMEOUT', 'UNREACHABLE', 'LOCAL_ERROR')),
    host_id     TEXT,
    detail      TEXT
) STRICT;

CREATE INDEX endpoint_results_endpoint ON endpoint_results (endpoint_id, ts);

-- Name records as advertised or resolved (§5.4). A record is linked to a
-- host only by evidence: the sender of a NAME, or the sole fresh holder of
-- an address record's address.
CREATE TABLE name_records (
    id             INTEGER PRIMARY KEY,
    context_id     INTEGER NOT NULL REFERENCES network_contexts (id),
    name           TEXT    NOT NULL,
    name_type      TEXT    NOT NULL CHECK (name_type IN ('mdns', 'dns', 'dns_ptr', 'dhcp', 'lldp')),
    record_type    TEXT    NOT NULL CHECK (record_type IN ('A', 'AAAA', 'PTR', 'SRV', 'TXT', 'CNAME', 'NAME')),
    address        TEXT,
    value          TEXT,
    source         TEXT    NOT NULL,
    advertiser_mac TEXT,
    advertiser_ip  TEXT,
    host_id        TEXT    REFERENCES hosts (host_id),
    link           TEXT    NOT NULL CHECK (link IN ('sender', 'address', 'none')),
    ttl_seconds    INTEGER,
    first_seen     INTEGER NOT NULL,
    last_seen      INTEGER NOT NULL,
    expires_at     INTEGER,
    ended_at       INTEGER,
    end_reason     TEXT,
    CHECK (last_seen >= first_seen),
    CHECK (ended_at IS NULL OR ended_at >= first_seen)
) STRICT;

CREATE UNIQUE INDEX name_records_open ON name_records (context_id, record_type, name COLLATE NOCASE, coalesce(address, ''),
    coalesce(value, ''), source, coalesce(host_id, ''), coalesce(advertiser_mac, '')) WHERE ended_at IS NULL;
CREATE INDEX name_records_host ON name_records (host_id, first_seen);
CREATE INDEX name_records_name ON name_records (name COLLATE NOCASE);
CREATE INDEX name_records_address ON name_records (context_id, address);

-- DHCP (§5.6). Servers per (context, server identifier, relay, sender MAC);
-- one row per exchange seen (context, xid, chaddr) with its messages,
-- offers, the server selected and the outcome; and per client the lease
-- timeline of the addresses and configurations servers acknowledged. A
-- lease is the server's statement: it creates no host and does not make
-- the client present. expires_at is the expiry per the last ACK seen, never
-- proof the address was released.
CREATE TABLE dhcp_servers (
    id          INTEGER PRIMARY KEY,
    context_id  INTEGER NOT NULL REFERENCES network_contexts (id),
    server_id   TEXT    NOT NULL,
    relay       TEXT    NOT NULL,
    mac         TEXT    NOT NULL,
    ip          TEXT    NOT NULL,
    host_id     TEXT    REFERENCES hosts (host_id),
    status      TEXT    NOT NULL CHECK (status IN ('allowed', 'unexpected', 'unchecked')),
    config_json TEXT    NOT NULL DEFAULT '{}',
    first_seen  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    UNIQUE (context_id, server_id, relay, mac),
    CHECK (last_seen >= first_seen)
) STRICT;

CREATE TABLE dhcp_exchanges (
    id              INTEGER PRIMARY KEY,
    context_id      INTEGER NOT NULL REFERENCES network_contexts (id),
    xid             INTEGER NOT NULL,
    chaddr          TEXT    NOT NULL,
    client_id       TEXT,
    host_id         TEXT    REFERENCES hosts (host_id),
    first_seen      INTEGER NOT NULL,
    last_seen       INTEGER NOT NULL,
    messages        TEXT    NOT NULL,
    requested_ip    TEXT,
    offers_json     TEXT    NOT NULL DEFAULT '[]',
    selected_server TEXT,
    outcome         TEXT    NOT NULL CHECK (outcome IN ('discover', 'offered', 'request', 'ack', 'inform', 'inform_ack', 'nak',
        'decline', 'release', 'incomplete')),
    visibility      TEXT    NOT NULL CHECK (visibility IN ('complete', 'partial')),
    relayed         INTEGER NOT NULL DEFAULT 0 CHECK (relayed IN (0, 1)),
    config_json     TEXT,
    lease_id        INTEGER,
    CHECK (last_seen >= first_seen)
) STRICT;

CREATE INDEX dhcp_exchanges_client ON dhcp_exchanges (context_id, chaddr, first_seen);
CREATE INDEX dhcp_exchanges_last ON dhcp_exchanges (last_seen);

CREATE TABLE dhcp_leases (
    id            INTEGER PRIMARY KEY,
    context_id    INTEGER NOT NULL REFERENCES network_contexts (id),
    chaddr        TEXT    NOT NULL,
    client_id     TEXT,
    host_id       TEXT    REFERENCES hosts (host_id),
    ip            TEXT    NOT NULL,
    server_id     TEXT    NOT NULL,
    sender_mac    TEXT    NOT NULL,
    sender_ip     TEXT    NOT NULL,
    relay         TEXT    NOT NULL,
    relay_circuit TEXT,
    relay_remote  TEXT,
    first_ack     INTEGER NOT NULL,
    last_ack      INTEGER NOT NULL,
    acks          INTEGER NOT NULL DEFAULT 1 CHECK (acks > 0),
    lease_seconds INTEGER,
    expires_at    INTEGER,
    t1_seconds    INTEGER,
    t2_seconds    INTEGER,
    t_derived     INTEGER NOT NULL DEFAULT 0 CHECK (t_derived IN (0, 1)),
    config_json   TEXT    NOT NULL DEFAULT '{}',
    partial       INTEGER NOT NULL DEFAULT 0 CHECK (partial IN (0, 1)),
    incomplete    INTEGER NOT NULL DEFAULT 0 CHECK (incomplete IN (0, 1)),
    ended_at      INTEGER,
    end_reason    TEXT,
    CHECK (last_ack >= first_ack),
    CHECK (ended_at IS NULL OR ended_at >= first_ack)
) STRICT;

CREATE INDEX dhcp_leases_client ON dhcp_leases (context_id, chaddr, first_ack);
CREATE INDEX dhcp_leases_ip ON dhcp_leases (context_id, ip, first_ack);
CREATE UNIQUE INDEX dhcp_leases_open ON dhcp_leases (context_id, chaddr) WHERE ended_at IS NULL;
