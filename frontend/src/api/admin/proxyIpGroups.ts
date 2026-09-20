import { apiClient } from '../client'
import type {
  CreateProxyIPGroupRequest,
  ProxyIPGroup,
  UpdateProxyIPGroupRequest
} from '@/types'

type ProxyIPGroupWire = Omit<ProxyIPGroup, 'member_count'> & { member_count?: number }
type ProxyIPGroupListResponse = ProxyIPGroupWire[] | { items: ProxyIPGroupWire[] }

function normalizeGroup(group: ProxyIPGroupWire): ProxyIPGroup {
  const proxyIds = Array.isArray(group.proxy_ids)
    ? Array.from(new Set(group.proxy_ids.filter(id => Number.isInteger(id) && id > 0)))
    : group.members?.map(member => member.id)
  const reportedCount = Number(group.member_count)
  const memberCount = proxyIds?.length ?? group.members?.length ?? (
    Number.isInteger(reportedCount) && reportedCount >= 0 ? reportedCount : 0
  )

  return {
    ...group,
    member_count: memberCount,
    ...(proxyIds ? { proxy_ids: proxyIds } : {})
  }
}

function normalizeList(data: ProxyIPGroupListResponse): ProxyIPGroup[] {
  if (Array.isArray(data)) return data.map(normalizeGroup)
  if (data && Array.isArray(data.items)) return data.items.map(normalizeGroup)
  throw new Error('Invalid proxy IP group list response')
}

export async function list(): Promise<ProxyIPGroup[]> {
  const { data } = await apiClient.get<ProxyIPGroupListResponse>('/admin/proxy-ip-groups')
  return normalizeList(data)
}

export async function getById(id: number): Promise<ProxyIPGroup> {
  const { data } = await apiClient.get<ProxyIPGroupWire>(`/admin/proxy-ip-groups/${id}`)
  return normalizeGroup(data)
}

export async function create(payload: CreateProxyIPGroupRequest): Promise<ProxyIPGroup> {
  const { data } = await apiClient.post<ProxyIPGroupWire>('/admin/proxy-ip-groups', payload)
  return normalizeGroup(data)
}

export async function update(id: number, payload: UpdateProxyIPGroupRequest): Promise<ProxyIPGroup> {
  const { data } = await apiClient.put<ProxyIPGroupWire>(`/admin/proxy-ip-groups/${id}`, payload)
  return normalizeGroup(data)
}

export async function updateMembers(id: number, proxyIds: number[]): Promise<ProxyIPGroup> {
  const { data } = await apiClient.put<ProxyIPGroupWire>(`/admin/proxy-ip-groups/${id}/members`, {
    proxy_ids: proxyIds
  })
  return normalizeGroup(data)
}

export async function deleteGroup(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.delete<{ message: string }>(`/admin/proxy-ip-groups/${id}`)
  return data
}

export const proxyIpGroupsAPI = {
  list,
  getById,
  create,
  update,
  updateMembers,
  delete: deleteGroup
}

export default proxyIpGroupsAPI
