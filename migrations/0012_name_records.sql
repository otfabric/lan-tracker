-- Name records (docs/DATA_MODEL.md §5.4): every name a context's traffic
-- advertised or a resolver answered, with its type, the address it
-- describes, who advertised it, its TTL and lifecycle. A record is linked
-- to a host (host_id) only by how link says: 'sender' for a name a device
-- gave itself (its DHCP hostname, its LLDP system name), 'address' when one
-- MAC alone held the described address, 'none' when no MAC did or several
-- did. The advertiser is never assumed to own what it advertises. Several
-- names per host and type are normal; expires_at is last_seen plus the TTL
-- and only marks a record expired when read; ended_at is set by an mDNS
-- goodbye or cache flush, or when another MAC took the address over.
CREATE TABLE name_records (
    id             INTEGER PRIMARY KEY,
    context_id     INTEGER NOT NULL REFERENCES network_contexts (id),
    name           TEXT    NOT NULL,
    name_type      TEXT    NOT NULL CHECK (name_type IN ('mdns', 'dns', 'dns_ptr', 'dhcp', 'lldp', 'netbios')),
    record_type    TEXT    NOT NULL CHECK (record_type IN ('A', 'AAAA', 'PTR', 'SRV', 'TXT', 'CNAME', 'NAME')),
    address        TEXT,
    value          TEXT,
    source         TEXT    NOT NULL,
    advertiser_mac TEXT,
    advertiser_ip  TEXT,
    host_id        TEXT    REFERENCES hosts (host_id),
    link           TEXT    NOT NULL CHECK (link IN ('sender', 'address', 'none', 'legacy')),
    ttl_seconds    INTEGER,
    first_seen     INTEGER NOT NULL,
    last_seen      INTEGER NOT NULL,
    expires_at     INTEGER,
    ended_at       INTEGER,
    end_reason     TEXT,
    legacy         INTEGER NOT NULL DEFAULT 0 CHECK (legacy IN (0, 1)),
    CHECK (last_seen >= first_seen),
    CHECK (ended_at IS NULL OR ended_at >= first_seen)
) STRICT;

CREATE UNIQUE INDEX name_records_open ON name_records (context_id, record_type, name COLLATE NOCASE, coalesce(address, ''),
    coalesce(value, ''), source, coalesce(host_id, ''), coalesce(advertiser_mac, '')) WHERE ended_at IS NULL;
CREATE INDEX name_records_host ON name_records (host_id, first_seen);
CREATE INDEX name_records_name ON name_records (name COLLATE NOCASE);
CREATE INDEX name_records_address ON name_records (context_id, address);

-- LAN Sentinel's names become last-known legacy records of the host they
-- were attached to: their record type, advertiser and TTL were never
-- captured, so they stay unknown, and a name LAN Sentinel replaced keeps
-- its end.
INSERT INTO name_records (context_id, name, name_type, record_type, source, host_id, link, first_seen, last_seen, ended_at,
    end_reason, legacy)
SELECT h.context_id, n.name, n.name_type, 'NAME', n.source, n.host_id, 'legacy', n.first_seen, n.last_seen, n.ended_at,
    CASE WHEN n.ended_at IS NULL THEN NULL ELSE 'replaced' END, 1
FROM names n JOIN hosts h ON h.host_id = n.host_id ORDER BY n.id;
