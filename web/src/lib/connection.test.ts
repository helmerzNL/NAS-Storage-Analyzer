import { describe, expect, it } from 'vitest';
import { bannerState, connectionState, isLoopback, warns, type ConnectionInfo } from './connection.svelte';
import { nl } from './i18n/nl';
import { en } from './i18n/en';

const info = (over: Partial<ConnectionInfo> = {}): ConnectionInfo => ({ scheme: 'https', require_https: true, via_trusted_proxy: true, ...over });
const http = (hostname: string) => ({ protocol: 'http:', hostname });
const https = (hostname: string) => ({ protocol: 'https:', hostname });

describe('isLoopback', () => {
  it('knows this computer by its names and addresses', () => {
    for (const h of ['localhost', 'LOCALHOST', 'app.localhost', '127.0.0.1', '127.8.9.10', '::1', '[::1]']) expect(isLoopback(h), h).toBe(true);
  });
  it('does not take a neighbour for this computer', () => {
    for (const h of ['nas', 'nas.lan', '192.168.1.10', '10.0.0.127', '127.0.0.1.example.com', 'localhost.example.com', 'notlocalhost', '::2', '']) expect(isLoopback(h), h).toBe(false);
  });
});

describe('connectionState', () => {
  it('warns about http to another machine, whatever the app says', () => {
    expect(connectionState(http('nas.lan'), null)).toBe('plain_http');
    expect(connectionState(http('192.168.1.10'), info({ scheme: 'http', require_https: false }))).toBe('plain_http');
    // A proxy that claims https while the browser itself used http: the address bar decides.
    expect(connectionState(http('nas.lan'), info())).toBe('plain_http');
  });
  it('lets http on this computer be', () => {
    expect(connectionState(http('localhost'), null)).toBe('local');
    expect(connectionState(http('127.0.0.1'), info({ scheme: 'http', require_https: false }))).toBe('local');
  });
  it('notices https that the app does not see', () => {
    expect(connectionState(https('nas.example.org'), info({ scheme: 'http', via_trusted_proxy: false }))).toBe('proxy_not_trusted');
    expect(connectionState(https('localhost'), info({ scheme: 'http' }))).toBe('proxy_not_trusted');
  });
  it('tells https that is not enforced from https that is', () => {
    expect(connectionState(https('nas.example.org'), info({ require_https: false }))).toBe('not_enforced');
    expect(connectionState(https('nas.example.org'), info())).toBe('ok');
  });
  it('says nothing about https before the app has answered', () => {
    expect(connectionState(https('nas.example.org'), null)).toBe('unknown');
  });
});

describe('the banner', () => {
  it('is for the two states that put the session at risk', () => {
    expect((['plain_http', 'proxy_not_trusted', 'not_enforced', 'local', 'ok', 'unknown'] as const).filter(warns)).toEqual(['plain_http', 'proxy_not_trusted']);
  });
  it('stays away once put away, and comes back when the state changes', () => {
    expect(bannerState(http('nas.lan'), null, '')).toBe('plain_http');
    expect(bannerState(http('nas.lan'), null, 'plain_http')).toBeNull();
    expect(bannerState(https('nas.example.org'), info({ scheme: 'http' }), 'plain_http')).toBe('proxy_not_trusted');
    expect(bannerState(https('nas.example.org'), info({ require_https: false }), '')).toBeNull();
    expect(bannerState(http('localhost'), null, '')).toBeNull();
  });
  // The sentences are looked up by state ('connection.' + state), which the check of keys in the source cannot see.
  it('has a title and an explanation for every state, in both languages', () => {
    for (const s of ['plain_http', 'proxy_not_trusted', 'not_enforced', 'local', 'ok', 'unknown'])
      for (const k of ['connection.' + s, 'connection.' + s + '.body']) { expect(k in nl, k).toBe(true); expect(k in en, k).toBe(true); }
    for (const s of ['plain_http', 'proxy_not_trusted']) { expect('connection.' + s + '.banner' in nl, s).toBe(true); expect('connection.' + s + '.banner' in en, s).toBe(true); }
  });
});
