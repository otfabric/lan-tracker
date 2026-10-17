-- DHCPv4 history (docs/DATA_MODEL.md §5.6), queryable apart from raw
-- observations so it survives their compaction.
--
-- One row per exchange seen: a client's transaction (context, xid, chaddr)
-- with every message in order (retransmissions counted), the offers of
-- each server, the server the client selected, the outcome and whether the
-- exchange was seen whole. A REQUEST without its ACK, or an ACK without its
-- REQUEST, is partial: on a switched port unicast messages are not seen.
-- config_json is what an ACK to an INFORM advertised (no allocation).
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

-- The lease timeline: one row per address and acknowledged configuration
-- a server asserted for a client (an ACK with yiaddr). A renewal with the
-- same address, server and configuration extends its row (last_ack,
-- acks, expires_at); anything else is a new row, the old one ended
-- 'superseded'. config_json is the immutable ACK snapshot: mask, routers,
-- DNS servers, domain, search list, hostname, FQDN, classless routes, the
-- gateway they imply and the options that were malformed; an option the ACK
-- did not carry is absent (unknown). expires_at is
-- the expiry per the last observed ACK (NULL: unknown or infinite), never
-- proof the address was released. The lease is the server's statement: it
-- creates no host and does not make the client present. Rows LAN Sentinel
-- left are backfilled where its raw observations still hold them, with
-- what its decoder did not capture unknown.
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
    backfilled    INTEGER NOT NULL DEFAULT 0 CHECK (backfilled IN (0, 1)),
    CHECK (last_ack >= first_ack),
    CHECK (ended_at IS NULL OR ended_at >= first_ack)
) STRICT;

CREATE INDEX dhcp_leases_client ON dhcp_leases (context_id, chaddr, first_ack);
CREATE INDEX dhcp_leases_ip ON dhcp_leases (context_id, ip, first_ack);

-- LAN Sentinel kept a server's lease ACK as a passive_dhcp_lease
-- observation (the client's MAC and yiaddr, the lease time and server
-- identifier). Each client's latest one becomes its open lease, the
-- earlier ones closed leases, all marked backfilled: the sender, relay,
-- configuration and T1/T2 were not captured, so they stay unknown.
INSERT INTO dhcp_leases (context_id, chaddr, client_id, host_id, ip, server_id, sender_mac, sender_ip, relay, first_ack, last_ack, acks,
    lease_seconds, expires_at, partial, ended_at, end_reason, backfilled)
SELECT context_id, mac, json_extract(meta_json, '$.client_id'), host_id, ip, coalesce(json_extract(meta_json, '$.server_id'), ''),
    '', '', '', min(ts), max(ts), count(*),
    CASE json_extract(meta_json, '$.lease_seconds') WHEN 'infinite' THEN -1 ELSE CAST(json_extract(meta_json, '$.lease_seconds') AS INTEGER) END,
    CASE WHEN json_extract(meta_json, '$.lease_seconds') GLOB '[0-9]*'
        THEN max(ts) + 1000 * CAST(json_extract(meta_json, '$.lease_seconds') AS INTEGER) END,
    1, NULL, NULL, 1
FROM observations WHERE source = 'passive_dhcp_lease' AND mac IS NOT NULL AND ip IS NOT NULL
GROUP BY context_id, mac, ip, coalesce(json_extract(meta_json, '$.server_id'), '')
ORDER BY min(ts);

-- Only each client's latest backfilled lease stays open.
UPDATE dhcp_leases SET ended_at = (SELECT min(l2.first_ack) FROM dhcp_leases l2 WHERE l2.context_id = dhcp_leases.context_id
        AND l2.chaddr = dhcp_leases.chaddr AND l2.first_ack > dhcp_leases.first_ack),
    end_reason = 'superseded'
WHERE backfilled = 1 AND EXISTS (SELECT 1 FROM dhcp_leases l2 WHERE l2.context_id = dhcp_leases.context_id
    AND l2.chaddr = dhcp_leases.chaddr AND l2.first_ack > dhcp_leases.first_ack);

-- One open lease per client.
CREATE UNIQUE INDEX dhcp_leases_open ON dhcp_leases (context_id, chaddr) WHERE ended_at IS NULL;
