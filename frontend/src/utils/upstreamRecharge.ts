interface UpstreamRechargeSource {
  provider: string
  site_url: string
}

// Use the upstream website, never the API endpoint or authentication material.
export function buildUpstreamRechargeURL(source: UpstreamRechargeSource): string | undefined {
  try {
    const url = new URL(source.site_url.trim())
    if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password) return undefined
    url.search = ''
    url.hash = ''
    const basePath = url.pathname.replace(/\/+$/, '')
    switch (source.provider) {
      case 'sub2api':
        url.pathname = `${basePath}/purchase`
        break
      case 'newapi':
        // Classic stable NewAPI; newer wallet UIs may use a different route.
        url.pathname = `${basePath}/console/topup`
        break
      default:
        // Unknown sites remain reachable without guessing their payment route.
        break
    }
    return url.toString()
  } catch {
    return undefined
  }
}
