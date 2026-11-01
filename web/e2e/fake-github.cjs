// A stand-in for github.com for the browser test: it answers /<repo>/releases/latest with a redirect to the tag page, like GitHub does, and
// records what the request carried, so the test can check that the update check sends nothing about the installation.
//   node fake-github.cjs <port>
// Control (from the test): GET /__tag?v=v1.2.3 sets the latest tag, GET /__hits counts the requests, GET /__last shows the last request's headers.
const http = require('http');
const REPO = '/helmerzNL/NAS-Storage-Analyzer';
let tag = 'v9.9.9', hits = 0, last = null;
http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x');
  if (u.pathname === '/__tag') { tag = u.searchParams.get('v'); res.writeHead(204).end(); return; }
  if (u.pathname === '/__hits') { res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ hits })); return; }
  if (u.pathname === '/__last') { res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify(last)); return; }
  if (u.pathname === REPO + '/releases/latest') {
    hits++; last = { url: req.url, headers: req.headers, at: Date.now() };
    res.writeHead(302, { Location: REPO + '/releases/tag/' + tag }).end();
    return;
  }
  res.writeHead(404).end();
}).listen(Number(process.argv[2] || 8098), '127.0.0.1');
