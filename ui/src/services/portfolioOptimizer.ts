// 投资组合中心 数据服务
// 调用后端 App.OptimizePortfolio / GetPortfolioOptimizations。
import { getAppInstance } from './api'

export type OptimizeMethod = 'mvo' | 'risk_parity' | 'risk_budget'

export interface OptimizeWeightItem {
  code: string
  name: string
  weight: number // 占投资组合比例 0-1
  riskPct: number // 风险贡献占比 0-1
  suggestAmt: number // 建议金额(元)
  annualVol: number
}

export interface OptimizeResult {
  code: string
  message?: string
  method: OptimizeMethod
  totalWeight: number
  volatility: number // 年化波动率 0-1
  annualReturn: number // 年化收益 0-1
  sharpe: number
  weights: OptimizeWeightItem[]
  isCurrentHold: boolean
  capital: number
  timestamp: string
}

export interface OptimizeRecord {
  id: number
  method: OptimizeMethod
  total_weight: number
  annual_return: number
  volatility: number
  sharpe: number
  is_current_hold: boolean
  remark: string
  created_at: string
  weights?: OptimizeWeightItem[]
}

export interface OptimizeRequest {
  method: OptimizeMethod
  symbols?: string[]
  totalWeight?: number
  maxSingle?: number
  budgets?: number[]
  days?: number
}

// 运行一次组合优化
export async function runOptimize(req: OptimizeRequest): Promise<OptimizeResult | null> {
  const app = getAppInstance()
  if (!app || typeof app['OptimizePortfolio'] !== 'function') return null
  try {
    return (await app['OptimizePortfolio'](req)) as OptimizeResult
  } catch (err) {
    console.error('OptimizePortfolio failed:', err)
    return null
  }
}

// 读取最近 limit 条优化记录
export async function getOptimizeRecords(limit = 10): Promise<OptimizeRecord[]> {
  const app = getAppInstance()
  if (!app || typeof app['GetPortfolioOptimizations'] !== 'function') return []
  try {
    const res = (await app['GetPortfolioOptimizations'](limit)) as { records?: OptimizeRecord[] }
    return res?.records ?? []
  } catch (err) {
    console.error('GetPortfolioOptimizations failed:', err)
    return []
  }
}

export const METHOD_LABELS: Record<OptimizeMethod, string> = {
  mvo: '均值-方差 (MVO)',
  risk_parity: '风险平价',
  risk_budget: '风险预算',
}

export const METHOD_DESC: Record<OptimizeMethod, string> = {
  mvo: '最大化夏普比率，收益导向，对预期收益误差敏感',
  risk_parity: '令各资产负债的波动风险贡献相等，天然去等权/集中，最稳健',
  risk_budget: '按给定风险预算反向解权重，兼顾可控性',
}

export function pct(v: number | undefined, digits = 2): string {
  if (v == null || !isFinite(v)) return '-'
  return `${(v * 100).toFixed(digits)}%`
}

export function money(v: number | undefined): string {
  if (v == null || !isFinite(v)) return '-'
  return `¥${v.toLocaleString('zh-CN', { maximumFractionDigits: 0 })}`
}