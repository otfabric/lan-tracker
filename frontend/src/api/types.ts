// The read model of the local API (docs/API.md; internal/store/model.go,
// internal/store/dhcp.go, internal/store/activity.go, internal/api). Field
// names are that contract.

export type Presence = 'ACTIVE' | 'RECENT' | 'STALE' | 'MISSING';

export interface HostSummary {
  host_id: string;
  interface: string;
  mac: string;
  oui_registrant?: string;
  locally_administered: boolean;
  presence: Presence;
  preferred_name?: string;
  proxy_arp?: boolean;
  description?: string;
  ips: string[];
  first_seen: string;
  last_seen: string;
  last_frame_seen?: string;
  last_address_confirmed?: string;
  last_endpoint_answer?: string;
}

export interface SourceSeen {
  source: string;
  first_seen: string;
  last_seen: string;
}

export interface Binding {
  ip: string;
  first_seen: string;
  last_seen: string;
  ended_at: string | null;
  conflict: boolean;
  sources?: SourceSeen[];
}

export interface NameRecord {
  name: string;
  type: string;
  record_type: string;
  address?: string;
  value?: string;
  source: string;
  advertiser_mac?: string;
  advertiser_ip?: string;
  link: string;
  host_id?: string;
  ttl_seconds?: number;
  first_seen: string;
  last_seen: string;
  expires_at?: string;
  ended_at: string | null;
  end_reason?: string;
  freshness: string;
}

export type TCPResult = 'OPEN' | 'REFUSED' | 'TIMEOUT' | 'UNREACHABLE' | 'LOCAL_ERROR';

export interface Endpoint {
  interface: string;
  ip: string;
  proto: string;
  port: number;
  host_id?: string;
  mac?: string;
  last_result: TCPResult;
  last_result_at: string;
  last_detail?: string;
  last_open_at?: string;
  last_definite_result?: string;
  last_definite_at?: string;
  first_seen: string;
}

export interface DHCPLease {
  id: number;
  interface: string;
  chaddr: string;
  client_id?: string;
  host_id?: string;
  ip: string;
  server_id?: string;
  sender_mac?: string;
  sender_ip?: string;
  relay?: string;
  first_ack: string;
  last_ack: string;
  acks: number;
  lease_seconds?: number;
  expires_at?: string;
  config: Record<string, unknown>;
  partial?: boolean;
  incomplete?: boolean;
  ended_at?: string;
  end_reason?: string;
}

export interface Host extends HostSummary {
  addresses: Binding[];
  names: NameRecord[];
  endpoints: Endpoint[];
  dhcp_leases: DHCPLease[];
}

export type Severity = 'info' | 'notice' | 'warning';

export interface LTEvent {
  id?: number;
  ts: string;
  type: string;
  severity: Severity;
  interface?: string;
  host_id?: string;
  mac?: string;
  related_host_id?: string;
  related_mac?: string;
  old?: string;
  new?: string;
  cause: string;
  evidence: Record<string, unknown> | null;
  clock_sync: string;
}

export interface DHCPServer {
  interface: string;
  server_id?: string;
  relay?: string;
  mac: string;
  ip?: string;
  host_id?: string;
  status: 'allowed' | 'unexpected' | 'unchecked';
  config: Record<string, string>;
  first_seen: string;
  last_seen: string;
}

export interface DHCPExchange {
  id: number;
  interface: string;
  xid: number;
  chaddr: string;
  host_id?: string;
  first_seen: string;
  last_seen: string;
  messages: string;
  requested_ip?: string;
  offers: { server_id?: string; yiaddr?: string; sender_mac?: string; count: number }[];
  selected_server?: string;
  outcome: string;
  visibility: string;
  relayed?: boolean;
}

export interface InterfaceInfo {
  name: string;
  state: string;
  mac?: string;
  prefixes: string[];
  passive: boolean;
  active: boolean;
  replay?: boolean;
  hosts: number;
  first_seen: string;
  last_seen: string;
}

export interface CollectorStatus {
  interface: string;
  collector: string;
  backend?: string;
  state: string;
  error?: string;
  since: string;
  last_pass?: { at: string; seconds: number; probed: number; replied: number; blocked?: number; complete: boolean; overran?: boolean };
}

export interface Status {
  version: string;
  commit: string;
  platform: string;
  pid: number;
  started: string;
  now: string;
  replay: boolean;
  state: 'ok' | 'degraded' | 'unhealthy';
  problems?: string[];
  database: { path: string; ok: boolean; error?: string; size: number; used_size: number; wal_size: number; schema_version: number };
  active: { disabled: boolean; reason?: string; by?: string; at?: string; forced?: boolean };
  clock: string;
  interfaces: (InterfaceInfo & { collectors: CollectorStatus[] })[] | null;
  hosts: Record<string, Partial<Record<Presence, number>>>;
  last_scan: { id: number; interface: string; kind: string; trigger: string; started: string; finished?: string; targets: number } | null;
}

export interface HostActivity {
  host_id: string;
  interface: string;
  mac: string;
  preferred_name?: string;
  description?: string;
  observations: number;
}

export interface Activity {
  since: string;
  until: string;
  step_seconds: number;
  buckets: number;
  events: Record<string, number[]>;
  observations: Record<string, number[]>;
  top_hosts: HostActivity[];
}

export interface Range {
  since: Date;
  until: Date;
}

export interface Scope {
  iface: string; // '' = every interface
  range: Range;
}

/** What the views read and change. HttpApi talks to the daemon; MockApi
 *  (development only) serves a generated site. */
export interface Api {
  status(): Promise<Status>;
  interfaces(): Promise<InterfaceInfo[]>;
  hosts(iface: string): Promise<HostSummary[]>;
  host(id: string): Promise<Host>;
  hostEvents(id: string, since: Date): Promise<LTEvent[]>;
  events(scope: Scope, limit?: number): Promise<LTEvent[]>;
  activity(scope: Scope, buckets: number, hostId?: string): Promise<Activity>;
  services(iface: string): Promise<Endpoint[]>;
  dhcpServers(iface: string): Promise<DHCPServer[]>;
  dhcpLeases(iface: string): Promise<DHCPLease[]>;
  dhcpExchanges(iface: string, since: Date): Promise<DHCPExchange[]>;
  setDescription(id: string, description: string): Promise<HostSummary>;
  /** Streams live events until the returned function is called. */
  stream(iface: string, onEvent: (e: LTEvent) => void, onState: (live: boolean) => void): () => void;
}
