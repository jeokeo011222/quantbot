// 研究中心-风险管理 数据服务
// 调用后端 App.RunRiskReport / GetRiskReports / GetRiskGuardStatus，
// 口径与风控师（intelligence.RiskEngine + policy.PolicyEngine）完全一致。
import { getAppInstance, safeCall } from './api'

// 组合风险报告（对应后端 riskcenter.Report）
export interface RiskReport {
  report_date: string
  source: string
  position_count: number
  total_exposure: number
  // 风险指标
  var_95: number
  var_99: number
  cvar_95: number
  volatility: number
  max_drawdown: number
  beta: number
  correlation: number
  concentration: number
  emd: number
  liquidity: number
  overall_score: number
  status: string // NORMAL / WATCH / WARNING / CRITICAL
  created_at?: string
  stress_tests?: StressTestLine[]
  limits?: CheckItem[]
  violations?: string[]
}

export interface StressTestLine {
  scenario: string
  description: string
  vol_multiplier: number
  shock: number
  estimated_loss: number
  probability: number
  passed: boolean // estimated_loss <= 15%
}

export interface CheckItem {
  name: string
  actual: number
  limit?: number
  passed: boolean
  message: string
}

export interface RiskGuardItem {
  key: string
  label: string
  status: string // armed / tripped / ok
  message: string
}

export interface RiskGuardStatus {
  emergency_stopped: boolean
  trading: boolean
  items: RiskGuardItem[]
  timestamp: string
}

// 运行一次组合风险报告
export async function runRiskReport(): Promise<RiskReport | null> {
  const app = getAppInstance()
  if (!app) return null
  try {
    return (await app.RunRiskReport()) as RiskReport
  } catch (err) {
    console.error('RunRiskReport failed:', err)
    return null
  }
}

// 读取最近 limit 条历史风险报告
export async function getRiskReports(limit = 7): Promise<RiskReport[]> {
  const app = getAppInstance()
  if (!app) return []
  try {
    const res = (await app.GetRiskReports(limit)) as { reports?: RiskReport[] }
    return res?.reports ?? []
  } catch (err) {
    console.error('GetRiskReports failed:', err)
    return []
  }
}

// 读取盘中熔断/风控守护状态
export async function getRiskGuardStatus(): Promise<RiskGuardStatus | null> {
  const app = getAppInstance()
  if (!app) return null
  try {
    return (await app.GetRiskGuardStatus()) as RiskGuardStatus
  } catch (err) {
    console.error('GetRiskGuardStatus failed:', err)
    return null
  }
}

// 读取最近一条每日复盘（盘后复盘板块），后端不可用时回退 null
export async function getLatestDailyReview(): Promise<Record<string, unknown> | null> {
  return safeCall<Record<string, unknown> | null>('GetLatestDailyReview', [], null, () => undefined)
}