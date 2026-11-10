import { describe, expect, it } from 'vitest';
import { describeAgent } from './agent';

describe('describeAgent', () => {
  const cases: [string, string, string][] = [
    ['Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36', 'Chrome', 'Windows'],
    ['Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36 Edg/142.0.0.0', 'Edge', 'Windows'],
    ['Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.1 Safari/605.1.15', 'Safari', 'macOS'],
    ['Mozilla/5.0 (X11; Linux x86_64; rv:144.0) Gecko/20100101 Firefox/144.0', 'Firefox', 'Linux'],
    ['Mozilla/5.0 (iPhone; CPU iPhone OS 19_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.1 Mobile/15E148 Safari/604.1', 'Safari', 'iPhone'],
    ['Mozilla/5.0 (iPhone; CPU iPhone OS 19_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/142.0.0.0 Mobile/15E148 Safari/604.1', 'Chrome', 'iPhone'],
    ['Mozilla/5.0 (iPad; CPU OS 19_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/144.0 Mobile/15E148 Safari/605.1.15', 'Firefox', 'iPad'],
    ['Mozilla/5.0 (Linux; Android 16; Pixel 10) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Mobile Safari/537.36', 'Chrome', 'Android'],
    ['Mozilla/5.0 (Linux; Android 16; SM-S938B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/29.0 Chrome/136.0.0.0 Mobile Safari/537.36', 'Samsung Internet', 'Android'],
    ['Mozilla/5.0 (X11; CrOS x86_64 16433.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36', 'Chrome', 'ChromeOS'],
    ['Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36 OPR/125.0.0.0', 'Opera', 'Windows'],
    // A browser driven by a test or a script names itself HeadlessChrome: still Chrome, and not the Safari it also mentions.
    ['Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/142.0.0.0 Safari/537.36', 'Chrome', 'Linux'],
    ['curl/8.14.1', 'curl', ''],
    ['Go-http-client/1.1', 'Go', ''],
  ];
  it('names the browser and the system people would recognise', () => {
    for (const [ua, browser, os] of cases) expect(describeAgent(ua), ua).toEqual({ browser, os });
  });
  it('says nothing when it does not know', () => {
    expect(describeAgent('')).toEqual({ browser: '', os: '' });
    expect(describeAgent('SomethingElse/3.2 (Plan 9)')).toEqual({ browser: '', os: '' });
  });
});
