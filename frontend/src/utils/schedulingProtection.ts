import type { UpstreamManagementSettings } from '@/api/admin/upstreamManagement'
import { extractApiErrorCode } from '@/utils/apiError'

export const SCHEDULING_PROTECTION_SETTINGS_UPDATED = 'scheduling-protection-settings-updated'

/** Missing legacy settings use OpenAI; an explicit empty list disables all platforms. */
export function schedulingProtectionPlatforms(settings: Pick<UpstreamManagementSettings, 'scheduling_protection_platforms'>): string[] {
  return [...new Set(settings.scheduling_protection_platforms ?? ['openai'])]
}

export function isSchedulingProtectionPlatformSelected(platform: string | undefined, selected: readonly string[]): boolean {
  return !!platform && platform !== 'composite' && selected.includes(platform)
}

export function isSchedulingProtectionPlatformError(error: unknown): boolean {
  const responseReason = (error as { response?: { data?: { reason?: string } } } | null)?.response?.data?.reason
  return (responseReason ?? extractApiErrorCode(error)) === 'GROUP_PROTECTION_PLATFORM_NOT_SELECTED'
}
