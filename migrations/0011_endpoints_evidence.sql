-- The connectivity model (docs/DATA_MODEL.md §2, §4, §5.5). See
-- docs/MIGRATION.md.
--
-- When each kind of evidence last refreshed a host: a frame from its MAC,
-- an address confirmed at it (an ARP answer, a REACHABLE neighbour entry),
-- an answer from an endpoint it held alone. last_seen stays the latest of
-- the three, which drives presence. Hosts from LAN Sentinel have them
-- unknown (NULL): its last_seen did not tell them apart.
ALTER TABLE hosts ADD COLUMN last_frame_seen INTEGER;
ALTER TABLE hosts ADD COLUMN last_address_confirmed INTEGER;
ALTER TABLE hosts ADD COLUMN last_endpoint_answer INTEGER;

-- TCP endpoints: one row per address and port on a context, so the same
-- port on two addresses of one MAC never overwrite each other. host_id is
-- the MAC that held the address alone at the last result (NULL when none
-- or several did). The last result and the last definite answer (OPEN or
-- REFUSED) are kept apart: a timeout does not erase that the endpoint last
-- answered OPEN. LAN Sentinel's services rows had no address and stay as
-- they are, as legacy host-level summaries; no address is invented for
-- them.
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

-- Every change of an endpoint's result or of the MAC it was attributed to,
-- for history and point-in-time reads. Repeated identical results are not
-- rows.
CREATE TABLE endpoint_results (
    id          INTEGER PRIMARY KEY,
    endpoint_id INTEGER NOT NULL REFERENCES endpoints (id),
    ts          INTEGER NOT NULL,
    result      TEXT    NOT NULL CHECK (result IN ('OPEN', 'REFUSED', 'TIMEOUT', 'UNREACHABLE', 'LOCAL_ERROR')),
    host_id     TEXT,
    detail      TEXT
) STRICT;

CREATE INDEX endpoint_results_endpoint ON endpoint_results (endpoint_id, ts);
