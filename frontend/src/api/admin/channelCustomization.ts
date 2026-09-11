/**
 * Aggregate channel customization settings.
 *
 * Rule order is significant: the gateway applies the first enabled rule that
 * matches both a target and every configured request-condition group.
 */

import { apiClient } from '../client'

export interface GatewayRequestObserverConfig {
  enabled: boolean
  api_key_ids: number[]
  api_key_names: string[]
  user_ids: number[]
  user_emails: string[]
  output_path?: string
}

export interface ChannelCustomizationQueryParams {
  [name: string]: string[]
}

export interface ChannelCustomizationRule {
  name: string
  enabled: boolean
  api_key_ids: number[]
  api_key_names: string[]
  user_ids: number[]
  user_emails: string[]
  methods: string[]
  exact_paths: string[]
  path_prefixes: string[]
  user_agent_contains: string[]
  query_params: ChannelCustomizationQueryParams
  request_message_match_mode?: 'exact' | 'regex'
  request_message_text?: string
  min_delay_ms: number
  max_delay_ms: number
  status_code: number
  content_type: string
  response_body: string
  hit_count?: number
}

export interface ChannelCustomizationSettings {
  observer: GatewayRequestObserverConfig
  rules: ChannelCustomizationRule[]
}

const endpoint = '/admin/settings/channel-customization'

export async function getSettings(): Promise<ChannelCustomizationSettings> {
  const { data } = await apiClient.get<ChannelCustomizationSettings>(endpoint)
  return data
}

export async function updateSettings(
  settings: ChannelCustomizationSettings,
): Promise<ChannelCustomizationSettings> {
  const { data } = await apiClient.put<ChannelCustomizationSettings>(endpoint, settings)
  return data
}

const channelCustomizationAPI = { getSettings, updateSettings }

export default channelCustomizationAPI
