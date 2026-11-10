// A name a person recognises for the browser of a session, from its User-Agent. Deliberately small: the handful of browsers and
// systems people use, and nothing when it is not sure. The full string stays available as a tooltip.

export interface Agent { browser: string; os: string }

const browsers: [RegExp, string][] = [
  // Order matters: most browsers also say "Chrome" and "Safari".
  [/\bEdg(?:e|A|iOS)?\//, 'Edge'], [/\bOPR\/|\bOpera\b/, 'Opera'], [/\bVivaldi\//, 'Vivaldi'], [/\bSamsungBrowser\//, 'Samsung Internet'],
  [/\bFirefox\/|\bFxiOS\//, 'Firefox'], [/Chrome\/|\bCriOS\/|\bChromium\//, 'Chrome'], [/\bSafari\//, 'Safari'],
  [/\bcurl\//, 'curl'], [/\bGo-http-client\//, 'Go'], [/\bpython-requests\/|\bPython-urllib\//, 'Python'],
];
const systems: [RegExp, string][] = [
  [/\biPhone\b|\biPod\b/, 'iPhone'], [/\biPad\b/, 'iPad'], [/\bAndroid\b/, 'Android'], [/\bCrOS\b/, 'ChromeOS'],
  [/\bWindows\b/, 'Windows'], [/\bMacintosh\b|\bMac OS X\b/, 'macOS'], [/\bLinux\b|\bX11\b/, 'Linux'],
];

export function describeAgent(userAgent: string): Agent {
  const find = (list: [RegExp, string][]) => list.find(([re]) => re.test(userAgent))?.[1] ?? '';
  return { browser: find(browsers), os: find(systems) };
}
