/**
 * Admin extra-cost ledger endpoints.
 *
 * Extra costs are append-only audit records (for example account purchases or
 * infrastructure expenses) and are kept separate from usage logs.
 */

import { apiClient } from '../client'

export type ExtraCostType = 'account' | 'proxy' | 'server' | 'other' | 'adjustment'

export interface ExtraCostEntry {
  id: number
  cost_date: string
  amount: number
  category: ExtraCostType
  notes: string
  created_by?: number | null
  created_at: string
  reversal_of?: number | null
}

export interface ExtraCostListParams {
  start_date?: string
  end_date?: string
  category?: ExtraCostType
  page?: number
  page_size?: number
}

export interface ExtraCostListResponse {
  items: ExtraCostEntry[]
  total: number
  page: number
  page_size: number
  daily_total?: number
  range_total?: number
}

export interface CreateExtraCostRequest {
  cost_date: string
  amount: number
  category: ExtraCostType
  notes?: string
  idempotency_key?: string
}

export interface ReverseExtraCostRequest {
  reason: string
  idempotency_key?: string
}

export async function list(params: ExtraCostListParams = {}): Promise<ExtraCostListResponse> {
  const { data } = await apiClient.get<ExtraCostListResponse>('/admin/extra-costs', { params })
  return data
}

export async function create(request: CreateExtraCostRequest): Promise<ExtraCostEntry> {
  const { data } = await apiClient.post<ExtraCostEntry>('/admin/extra-costs', request, {
    headers: request.idempotency_key ? { 'X-Idempotency-Key': request.idempotency_key } : undefined
  })
  return data
}

export async function reverse(id: number, request: ReverseExtraCostRequest): Promise<ExtraCostEntry> {
  const { data } = await apiClient.post<ExtraCostEntry>(`/admin/extra-costs/${id}/reverse`, request, {
    headers: request.idempotency_key ? { 'X-Idempotency-Key': request.idempotency_key } : undefined
  })
  return data
}

const extraCostsAPI = { list, create, reverse }

export default extraCostsAPI
