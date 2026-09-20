export interface Bridge {
  request(type: string, payload?: Record<string, unknown>): Promise<Record<string, unknown>>
  notify(type: string, payload?: Record<string, unknown>): void
  dispose(): void
}

export function createBridge(target: Window = window): Bridge {
  const token = new URLSearchParams(target.location.hash.slice(1)).get('bridge_token') || ''
  let sequence = 0
  let disposed = false
  const pending = new Map<string, {
    type: string
    resolve: (data: Record<string, unknown>) => void
    reject: (reason: Error) => void
    timer: ReturnType<typeof setTimeout>
  }>()
  const send = (type: string, payload: Record<string, unknown>) => {
    target.parent.postMessage({ ...payload, source: 'sub2api-plugin-ui', bridge_token: token, type }, '*')
  }
  const receive = (event: MessageEvent) => {
    const data = event.data
    if (event.source !== target.parent || !data || data.source !== 'sub2api-plugin-host' || data.bridge_token !== token) return
    const request = pending.get(data.request_id)
    if (!request || data.type !== `${request.type}.result`) return
    clearTimeout(request.timer)
    pending.delete(data.request_id)
    // Host errors are intentionally opaque: upstream messages may contain credentials.
    if (data.ok === true) request.resolve(data)
    else request.reject(new Error(request.type === 'plugin.secrets.edit' && data.code === 'cancelled' ? 'bridge_cancelled' : 'bridge_request_failed'))
  }
  target.addEventListener('message', receive)
  return {
    request(type, payload = {}) {
      if (disposed || !token || target.parent === target) return Promise.reject(new Error('bridge_unavailable'))
      if (type === 'plugin.secrets.edit' && Object.keys(payload).length) return Promise.reject(new Error('bridge_request_failed'))
      const request_id = `${Date.now().toString(36)}-${++sequence}`
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => {
          pending.delete(request_id)
          reject(new Error('bridge_timeout'))
        }, type === 'plugin.secrets.edit' ? 10 * 60_000 : 30_000)
        pending.set(request_id, { type, resolve, reject, timer })
        send(type, { ...payload, request_id })
      })
    },
    notify(type, payload = {}) { if (!disposed && token) send(type, payload) },
    dispose() {
      disposed = true
      target.removeEventListener('message', receive)
      for (const request of pending.values()) {
        clearTimeout(request.timer)
        request.reject(new Error('bridge_closed'))
      }
      pending.clear()
    },
  }
}
