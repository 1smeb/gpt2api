import { http } from './http'

export interface EPayConfig {
  gateway_url: string
  pid: string
  notify_url: string
  return_url: string
  sign_type: string
  key_set: boolean
  key_mask: string
  effective_notify_url: string
  effective_return_url: string
  channel_ready: boolean
  recharge_enabled: boolean
}

export interface EPayConfigUpdate {
  gateway_url: string
  pid: string
  key?: string
  notify_url: string
  return_url: string
  sign_type: string
  recharge_enabled: boolean
}

export function getEPayConfig(): Promise<EPayConfig> {
  return http.get('/api/admin/payment/epay')
}

export function updateEPayConfig(payload: EPayConfigUpdate): Promise<EPayConfig> {
  return http.put('/api/admin/payment/epay', payload)
}
