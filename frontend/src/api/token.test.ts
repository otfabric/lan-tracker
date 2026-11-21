import { describe, expect, it, vi } from 'vitest';
import { readToken, takeToken } from './token';

describe('token', () => {
  it('reads the token from the fragment', () => {
    expect(readToken('#token=abc')).toBe('abc');
    expect(readToken('')).toBeNull();
    expect(readToken('#token=')).toBeNull();
  });

  it('takes the token out of the address bar', () => {
    const replaceState = vi.fn();
    const win = { location: { hash: '#token=abc', pathname: '/hosts', search: '?x=1' }, history: { state: null, replaceState } };
    expect(takeToken(win as unknown as Window)).toBe('abc');
    expect(replaceState).toHaveBeenCalledWith(null, '', '/hosts?x=1');
  });

  it('leaves the address alone without a token', () => {
    const replaceState = vi.fn();
    const win = { location: { hash: '', pathname: '/', search: '' }, history: { state: null, replaceState } };
    expect(takeToken(win as unknown as Window)).toBeNull();
    expect(replaceState).not.toHaveBeenCalled();
  });
});
