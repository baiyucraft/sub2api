import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, put } = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get, put } }))

import * as channelCustomization from '../channelCustomization'

const settings: channelCustomization.ChannelCustomizationSettings = {
  observer: {
    enabled: true,
    api_key_ids: [12],
    api_key_names: ['maibon-gpt'],
    user_ids: [34],
    user_emails: ['1069167864@qq.com'],
    output_path: '/app/.tmp/maibon-probe-observation/requests.jsonl',
  },
  rules: [
    {
      name: 'maibon probe',
      enabled: true,
      api_key_ids: [12],
      api_key_names: ['maibon-gpt'],
      user_ids: [],
      user_emails: [],
      methods: ['GET'],
      exact_paths: ['/v1/models'],
      path_prefixes: [],
      user_agent_contains: ['probe'],
      query_params: { check: ['health', 'ready'] },
      min_delay_ms: 100,
      max_delay_ms: 300,
      status_code: 200,
      content_type: 'application/json',
      response_body: '{"ok":true}',
    },
  ],
}

describe('admin channel customization API', () => {
  beforeEach(() => vi.clearAllMocks())

  it('loads and saves the aggregate settings document', async () => {
    get.mockResolvedValueOnce({ data: settings })
    put.mockResolvedValueOnce({ data: settings })

    await expect(channelCustomization.getSettings()).resolves.toEqual(settings)
    await expect(channelCustomization.updateSettings(settings)).resolves.toEqual(settings)

    expect(get).toHaveBeenCalledWith('/admin/settings/channel-customization')
    expect(put).toHaveBeenCalledWith('/admin/settings/channel-customization', settings)
  })
})
