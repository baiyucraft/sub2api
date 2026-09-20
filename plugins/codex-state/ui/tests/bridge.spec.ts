import { afterEach, describe, expect, it, vi } from 'vitest'
import { createBridge } from '../src/bridge'

function harness() {
  const postMessage = vi.fn()
  const addEventListener = vi.fn()
  const removeEventListener = vi.fn()
  const parent = { postMessage }
  const target = { parent, location: { hash: '#bridge_token=limited-capability' }, addEventListener, removeEventListener } as unknown as Window
  const bridge = createBridge(target)
  const receive = (data: unknown, source: unknown = parent) => addEventListener.mock.calls[0][1]({ source, data })
  return { bridge, receive, postMessage, removeEventListener }
}

afterEach(() => vi.useRealTimers())
describe('isolated plugin bridge client', () => {
  it('allows an interactive secret editor timeout and rejects value-carrying requests', async () => {
    vi.useFakeTimers()
    const { bridge, postMessage, receive } = harness()
    await expect(bridge.request('plugin.secrets.edit', { values: { token: 'PRIVATE' } })).rejects.toThrow('bridge_request_failed')
    expect(postMessage).not.toHaveBeenCalled()
    const editing = bridge.request('plugin.secrets.edit')
    const rejected = expect(editing).rejects.toThrow('bridge_cancelled')
    const request = postMessage.mock.calls[0][0]
    await vi.advanceTimersByTimeAsync(31_000)
    receive({ ...request, source: 'sub2api-plugin-host', type: 'plugin.secrets.edit.result', ok: false, code: 'cancelled' })
    await rejected
    const expired = expect(bridge.request('plugin.secrets.edit')).rejects.toThrow('bridge_timeout')
    await vi.advanceTimersByTimeAsync(10 * 60_000)
    await expired
    bridge.dispose()
  })
  it('validates parent, token, pending ID and response type', async () => {
    const { bridge, receive, postMessage } = harness()
    const promise = bridge.request('config.load')
    const request = postMessage.mock.calls[0][0]
    const response = { source: 'sub2api-plugin-host', bridge_token: 'limited-capability', type: 'config.load.result', request_id: request.request_id, ok: true, config: { enabled: false } }
    const resolved = vi.fn()
    void promise.then(resolved)
    receive(response, {})
    receive({ ...response, bridge_token: 'wrong' })
    receive({ ...response, request_id: 'wrong' })
    receive({ ...response, type: 'plugin.status.result' })
    await Promise.resolve()
    expect(resolved).not.toHaveBeenCalled()
    receive(response)
    expect(await promise).toEqual(response)
    expect(request).not.toHaveProperty('admin_token')
    bridge.dispose()
  })

  it('times out requests and cleans up outstanding promises on unload', async () => {
    vi.useFakeTimers()
    const { bridge, removeEventListener } = harness()
    const timed = bridge.request('plugin.resources')
    const timeout = expect(timed).rejects.toThrow('bridge_timeout')
    await vi.advanceTimersByTimeAsync(30000)
    await timeout
    const open = bridge.request('plugin.status')
    const closed = expect(open).rejects.toThrow('bridge_closed')
    bridge.dispose()
    await closed
    expect(removeEventListener).toHaveBeenCalledTimes(1)
    await expect(bridge.request('config.save')).rejects.toThrow('bridge_unavailable')
  })

  it('does not reflect host errors that contain secrets', async () => {
    const { bridge, receive, postMessage } = harness()
    const promise = bridge.request('plugin.action')
    receive({ ...postMessage.mock.calls[0][0], source: 'sub2api-plugin-host', type: 'plugin.action.result', ok: false, error: 'SECRET' })
    await expect(promise).rejects.toThrow('bridge_request_failed')
    bridge.dispose()
  })
})
