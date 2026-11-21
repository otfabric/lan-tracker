-- Per-interface host cap (ADR 0013, docs/DATA_MODEL.md §5.9).
--
-- hosts.evicted_at: when the correlator stopped holding the host in its
-- current state to make room for a new MAC on a full interface. The row and
-- its history stay; the same MAC seen again on that interface is the same
-- host and clears it.
ALTER TABLE hosts ADD COLUMN evicted_at INTEGER;

-- The event catalogue gains HOST_EVICTED and HOST_CAP_REACHED. A CHECK
-- constraint cannot be altered, so events is rebuilt (docs/COMMON_TASKS.md).
-- Nothing references events, so dropping it touches no foreign key.
CREATE TABLE events_rebuilt (
    id              INTEGER PRIMARY KEY,
    ts              INTEGER NOT NULL,
    type            TEXT    NOT NULL CHECK (type IN (
        'HOST_DISCOVERED', 'HOST_DISAPPEARED', 'HOST_REAPPEARED', 'HOST_DESCRIBED', 'HOST_EVICTED', 'HOST_CAP_REACHED',
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

INSERT INTO events_rebuilt (id, ts, type, severity, context_id, host_id, related_host_id, old_value, new_value, cause,
    observation_id, evidence_json, clock_sync)
SELECT id, ts, type, severity, context_id, host_id, related_host_id, old_value, new_value, cause,
    observation_id, evidence_json, clock_sync FROM events;

DROP TABLE events;
ALTER TABLE events_rebuilt RENAME TO events;

CREATE INDEX events_ts ON events (ts);
CREATE INDEX events_host ON events (host_id, ts);
CREATE INDEX events_type ON events (type, ts);
CREATE INDEX events_context ON events (context_id, ts);
CREATE INDEX events_related ON events (related_host_id, ts);
CREATE INDEX events_context_type ON events (context_id, type, ts);
CREATE INDEX events_old ON events (old_value);
CREATE INDEX events_new ON events (new_value);
CREATE INDEX events_evidence_ip ON events (json_extract(evidence_json, '$.ip'));
