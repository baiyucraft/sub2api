const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const root = path.resolve(__dirname, '../../../../../../../backend/internal/web/dist');
if (!fs.existsSync(path.join(root,'index.html'))) throw new Error('Missing production build; run pnpm build from frontend');
const mime = { '.html':'text/html', '.js':'text/javascript', '.css':'text/css', '.svg':'image/svg+xml', '.png':'image/png', '.woff2':'font/woff2' };
http.createServer((req,res) => {
  const pathname = decodeURIComponent(new URL(req.url, 'http://127.0.0.1').pathname);
  let file = path.resolve(root, '.' + pathname);
  if (!file.startsWith(root + path.sep) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) file = path.join(root,'index.html');
  res.writeHead(200, {'Content-Type':mime[path.extname(file)] || 'application/octet-stream', 'Cache-Control':'no-store'});
  fs.createReadStream(file).pipe(res);
}).listen(4319, '127.0.0.1', () => console.log('fixture-static http://127.0.0.1:4319'));
