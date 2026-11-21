import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError, HttpApi, readLines } from './http';

function body(chunks: string[]): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  return new ReadableStream({
    start(c) {
      for (const ch of chunks) c.enqueue(enc.encode(ch));
      c.close();
    },
  });
}

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } });

afterEach(() => vi.restoreAllMocks());

describe('readLines', () => {
  it('joins lines split across chunks and skips blank ones', async () => {
    const got: string[] = [];
    await readLines(body(['{"a":1}\n{"b"', ':2}\n\n', '{"c":3}']), (l) => got.push(l));
    expect(got).toEqual(['{"a":1}', '{"b":2}', '{"c":3}']);
  });
});

describe('HttpApi', () => {
  it('sends the bearer token and the query, and drops empty parameters', async () => {
    const f = vi.spyOn(globalThis, 'fetch').mockImplementation(() => Promise.resolve(json([])));
    await new HttpApi('tok').hosts('');
    await new HttpApi('tok').services('eth1');
    expect(f.mock.calls[0]![0]).toBe('/v1/hosts');
    expect(f.mock.calls[1]![0]).toBe('/v1/services?interface=eth1');
    expect((f.mock.calls[0]![1] as RequestInit).headers).toEqual({ Authorization: 'Bearer tok' });
  });

  it('turns a 401 into an auth error with the daemon message', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(json({ error: 'token required', code: 'token' }, 401));
    const err = await new HttpApi(null).status().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).auth).toBe(true);
    expect((err as ApiError).message).toBe('token required');
  });

  it('keeps the status line when the error body is not JSON', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('boom', { status: 500, statusText: 'Internal Server Error' }));
    const err = (await new HttpApi('t').status().catch((e: unknown) => e)) as ApiError;
    expect(err.auth).toBe(false);
    expect(err.message).toBe('500 Internal Server Error');
  });

  it('posts a description as JSON to the escaped host path', async () => {
    const f = vi.spyOn(globalThis, 'fetch').mockResolvedValue(json({ host_id: 'a/b' }));
    await new HttpApi('t').setDescription('a/b', 'Pump 3');
    const [url, init] = f.mock.calls[0]! as [string, RequestInit];
    expect(url).toBe('/v1/hosts/a%2Fb/description');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ description: 'Pump 3' });
  });

  it('streams events, then reports the stream down when it ends', async () => {
    vi.useFakeTimers();
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(new Response(body(['{"type":"HOST_NEW","ts":"x"}\n']))).mockResolvedValue(json({ error: 'no' }, 401));
    const events: string[] = [];
    const states: boolean[] = [];
    const stop = new HttpApi('t').stream('', (e) => events.push(e.type), (s) => states.push(s));
    await vi.waitFor(() => expect(states).toEqual([true, false]));
    await vi.advanceTimersByTimeAsync(1000);
    await vi.waitFor(() => expect(states).toEqual([true, false, false]));
    stop();
    vi.useRealTimers();
    expect(events).toEqual(['HOST_NEW']);
  });
});
