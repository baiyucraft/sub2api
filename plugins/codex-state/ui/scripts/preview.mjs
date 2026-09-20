import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
const root = new URL('../', import.meta.url)
const routes = new Map([
  ['/', ['tests/preview-host.html', 'text/html; charset=utf-8']],
  ['/dist/index.html', ['dist/index.html', 'text/html; charset=utf-8']],
  ['/dist/app.js', ['dist/app.js', 'application/javascript']],
  ['/dist/style.css', ['dist/style.css', 'text/css']],
])
const server = createServer(async (request, response) => {
  const route = routes.get(new URL(request.url, 'http://localhost').pathname)
  if (!route) { response.writeHead(404).end(); return }
  response.setHeader('Content-Type', route[1])
  response.setHeader('Cross-Origin-Resource-Policy', 'cross-origin')
  if (request.url.startsWith('/dist/')) response.setHeader('Content-Security-Policy', "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'")
  try { response.end(await readFile(fileURLToPath(new URL(route[0], root)))) }
  catch { response.writeHead(500).end('Build the local UI first.') }
})
server.listen(Number(process.env.STATE_UI_PREVIEW_PORT || 4319), '127.0.0.1', () => console.log(`Local fixture: http://127.0.0.1:${server.address().port}`))
