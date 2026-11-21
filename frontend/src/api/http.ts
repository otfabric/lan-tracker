import type { Activity, Api, DHCPExchange, DHCPLease, DHCPServer, Endpoint, Host, HostSummary, InterfaceInfo, LTEvent, Scope, Status } from './types';

/** An API error with the daemon's message; `auth` is a missing or wrong token. */
export class ApiError extends Error {
  readonly status: number;
  readonly auth: boolean;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.auth = status === 401;
  }
}

type Query = Record<string, string | number | undefined>;

function qs(q: Query): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== '') p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : '';
}

const iso = (d: Date) => d.toISOString();

/** The daemon's API on the UI listener, with the bearer token from the URL
 *  fragment (kept in memory only). */
export class HttpApi implements Api {
  private readonly token: string | null;
  private readonly base: string;

  constructor(token: string | null, base = '') {
    this.token = token;
    this.base = base;
  }

  private headers(): HeadersInit {
    return this.token ? { Authorization: `Bearer ${this.token}` } : {};
  }

  private async get<T>(path: string, q: Query = {}): Promise<T> {
    const resp = await fetch(this.base + path + qs(q), { headers: this.headers(), cache: 'no-store' });
    if (!resp.ok) throw await failure(resp);
    return (await resp.json()) as T;
  }

  status() {
    return this.get<Status>('/v1/status');
  }
  interfaces() {
    return this.get<InterfaceInfo[]>('/v1/interfaces');
  }
  hosts(iface: string) {
    return this.get<HostSummary[]>('/v1/hosts', { interface: iface });
  }
  host(id: string) {
    return this.get<Host>(`/v1/hosts/${encodeURIComponent(id)}`);
  }
  hostEvents(id: string, since: Date) {
    return this.get<LTEvent[]>(`/v1/hosts/${encodeURIComponent(id)}/history`, { since: iso(since) });
  }
  events(scope: Scope, limit = 2000) {
    return this.get<LTEvent[]>('/v1/events', { interface: scope.iface, since: iso(scope.range.since), until: iso(scope.range.until), limit });
  }
  activity(scope: Scope, buckets: number, hostId?: string) {
    return this.get<Activity>('/v1/activity', {
      interface: hostId ? undefined : scope.iface,
      host: hostId,
      since: iso(scope.range.since),
      until: iso(scope.range.until),
      buckets,
    });
  }
  services(iface: string) {
    return this.get<Endpoint[]>('/v1/services', { interface: iface });
  }
  dhcpServers(iface: string) {
    return this.get<DHCPServer[]>('/v1/dhcp/servers', { interface: iface });
  }
  dhcpLeases(iface: string) {
    return this.get<DHCPLease[]>('/v1/dhcp/leases', { interface: iface, limit: 500 });
  }
  dhcpExchanges(iface: string, since: Date) {
    return this.get<DHCPExchange[]>('/v1/dhcp/exchanges', { interface: iface, since: iso(since), limit: 1000 });
  }

  async setDescription(id: string, description: string): Promise<HostSummary> {
    const resp = await fetch(`${this.base}/v1/hosts/${encodeURIComponent(id)}/description`, {
      method: 'POST',
      headers: { ...this.headers(), 'Content-Type': 'application/json' },
      body: JSON.stringify({ description }),
    });
    if (!resp.ok) throw await failure(resp);
    return (await resp.json()) as HostSummary;
  }

  stream(iface: string, onEvent: (e: LTEvent) => void, onState: (live: boolean) => void): () => void {
    const ctrl = new AbortController();
    let stopped = false;
    let retry = 1000;
    const run = async () => {
      while (!stopped) {
        try {
          const resp = await fetch(this.base + '/v1/events/stream' + qs({ interface: iface }), {
            headers: this.headers(),
            signal: ctrl.signal,
          });
          if (!resp.ok || !resp.body) throw await failure(resp);
          onState(true);
          retry = 1000;
          await readLines(resp.body, (line) => onEvent(JSON.parse(line) as LTEvent));
        } catch (err) {
          if (stopped) return;
          if (err instanceof ApiError && err.auth) {
            onState(false);
            return;
          }
        }
        onState(false);
        // Reconnect with a capped back-off: the daemon restarts, or the SSH
        // tunnel drops.
        await new Promise((r) => setTimeout(r, retry));
        retry = Math.min(retry * 2, 15000);
      }
    };
    void run();
    return () => {
      stopped = true;
      ctrl.abort();
    };
  }
}

async function failure(resp: Response): Promise<ApiError> {
  let msg = `${resp.status} ${resp.statusText}`;
  try {
    const body = (await resp.json()) as { error?: string };
    if (body.error) msg = body.error;
  } catch {
    /* not JSON */
  }
  return new ApiError(resp.status, msg);
}

/** Calls fn with each complete line of a JSON-lines body. */
export async function readLines(body: ReadableStream<Uint8Array>, fn: (line: string) => void): Promise<void> {
  const reader = body.getReader();
  const dec = new TextDecoder();
  let buf = '';
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    let i: number;
    while ((i = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, i).trim();
      buf = buf.slice(i + 1);
      if (line) fn(line);
    }
  }
  if (buf.trim()) fn(buf.trim());
}
