import { describe, expect, it } from 'vitest';
import { EVENT_CATEGORIES, ago, bytes, compact, duration, eventCategory, eventTypeParam, group, hostLabel } from './format';

describe('format', () => {
  it('keeps only event types of the catalogue form', () => {
    expect(eventTypeParam('IP_ADDED')).toBe('IP_ADDED');
    expect(eventTypeParam('DHCP_LEASE_ASSIGNED')).toBe('DHCP_LEASE_ASSIGNED');
    for (const bad of [null, undefined, '', 'ip_added', 'ip-added', 'IP_ADDED; rm -rf /', 'IP ADDED', '$(id)', '_IP', '1IP']) {
      expect(eventTypeParam(bad)).toBe('');
    }
  });

  it('says how long ago', () => {
    const now = Date.parse('2026-11-21T12:00:00Z');
    expect(ago(undefined, now)).toBe('—');
    expect(ago('2026-11-21T11:59:30Z', now)).toBe('just now');
    expect(ago('2026-11-21T11:50:00Z', now)).toBe('10 min ago');
    expect(ago('2026-11-21T09:00:00Z', now)).toBe('3 h ago');
    expect(ago('2026-11-18T12:00:00Z', now)).toBe('3 d ago');
  });

  it('formats sizes, counts and durations', () => {
    expect(bytes(512)).toBe('512 B');
    expect(bytes(58_900_000)).toBe('58.9 MB');
    expect(compact(104_000)).toBe('104k');
    expect(duration(30_000)).toBe('30 s');
    expect(duration(5_400_000)).toBe('1.5 h');
  });

  it('files an unknown event type under a category without failing', () => {
    expect(eventCategory('HOST_DISCOVERED').key).toBe('devices');
    expect(eventCategory('SOMETHING_NEW').key).toBe('tracker');
  });

  it('groups per-type counts into categories', () => {
    const g = group({ HOST_DISCOVERED: [1, 2], HOST_REAPPEARED: [3], IP_ADDED: [5, 5] }, EVENT_CATEGORIES, 2);
    expect(g.find((x) => x.cat.key === 'devices')!.values).toEqual([4, 2]);
    expect(g.find((x) => x.cat.key === 'addresses')!.values).toEqual([5, 5]);
    expect(g.find((x) => x.cat.key === 'names')!.values).toEqual([0, 0]);
  });

  it('labels a host by name, then description, then MAC', () => {
    expect(hostLabel({ mac: 'm', description: 'd', preferred_name: 'n' })).toBe('n');
    expect(hostLabel({ mac: 'm', description: 'd' })).toBe('d');
    expect(hostLabel({ mac: 'm' })).toBe('m');
  });
});
