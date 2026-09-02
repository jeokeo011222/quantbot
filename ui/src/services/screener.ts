// 选股引擎 API 服务

export interface FactorScore {
  factorId: string
  factorName: string
  score: number
  breakdown: Record<string, number>
  contributor?: string
}

export interface StockScore {
  code: string
  name: string
  market: string
  price: number
  changePct: number
  totalScore: number
  ranking: number
  reasons: string[]
  warnings: string[]
  factorScores: FactorScore[]
  // Pro版扩展 - 收益率和风险指标
  momentum1m?: number
  momentum3m?: number
  momentum6m?: number
  momentum12m?: number
  volatility?: number
  amplitude?: number
  turnoverRate?: number
  liquidity?: number
}

export interface ScreeningResult {
  strategyName: string
  totalCount: number
  results: StockScore[]
  generatedAt: string
  formula: string
  dynamicFormula?: string
  tier: string
  upgrades: string[]
  dynamicWeights?: Record<string, number>
  templateWeights?: Record<string, number>
}

export interface StrategyTemplate {
  id: string
  name: string
  description: string
  icon: string
  riskLevel: string
  weights: Record<string, number>
}

export interface TierInfo {
  tier: string
  name: string
  displayName: string
  description: string
  features: string[]
  limits: {
    maxScreeningPerDay: number
    maxResults: number
    allowCustomWeights: boolean
    allowFactorHealth: boolean
    allowDynamicWeight: boolean
    allowStructureRisk: boolean
    allowPortfolioOpt: boolean
    allowAIAgent: boolean
    allowPrivateFactor: boolean
    allowPrivateDeploy: boolean
    allowCustomStrategy: boolean
  }
}

// 市场数据质量状态（选股引擎数据底座健康度）
export interface MarketDataStatus {
  ok: boolean
  available: boolean
  fresh: boolean
  latest_date: string
  current_date: string
  stock_count: number
  missing_rate: number
  data_source: string
  days_since_last: number
  reason: string
  checked_at: string
}

export interface UpgradePath {
  free: { name: string; description: string; features: string[] }
  pro: { name: string; description: string; features: string[] }
  enterprise: { name: string; description: string; features: string[] }
}

const BACKEND_UNAVAILABLE_MSG = 'QuantBot 后端服务未启动，请重启应用后重试'

function getApp(): any {
  return (window as any)['go']?.['main']?.['App']
}

// 调用后端选股 API
export async function screenStock(
  strategyID: string,
  market: string = 'all',
  maxResults: number = 50,
  minScore: number = 0,
  customWeights?: Record<string, number>
): Promise<ScreeningResult> {
  const App = getApp()

  if (!App || typeof App.ScreenStock !== 'function') {
    throw new Error(BACKEND_UNAVAILABLE_MSG)
  }

  const weightsJSON = customWeights ? JSON.stringify(customWeights) : ''

  try {
    const result = await App.ScreenStock(strategyID, market, maxResults, minScore, weightsJSON)
    return result
  } catch (err) {
    console.error('Screening API failed:', err)
    throw new Error(`选股请求失败: ${err instanceof Error ? err.message : String(err)}`)
  }
}

// 获取策略模板列表
export async function getStrategyList(): Promise<StrategyTemplate[]> {
  const App = getApp()

  if (!App || typeof App.GetStrategyList !== 'function') {
    throw new Error(BACKEND_UNAVAILABLE_MSG)
  }

  try {
    const result = await App.GetStrategyList()
    return result.strategies
  } catch (err) {
    console.error('GetStrategyList failed:', err)
    throw new Error(`获取策略列表失败: ${err instanceof Error ? err.message : String(err)}`)
  }
}

// 获取市场数据质量状态（选股引擎数据底座健康度）
export async function getMarketDataStatus(): Promise<MarketDataStatus> {
  const App = getApp()

  if (!App || typeof App.GetMarketDataStatus !== 'function') {
    throw new Error(BACKEND_UNAVAILABLE_MSG)
  }

  try {
    return await App.GetMarketDataStatus()
  } catch (err) {
    console.error('GetMarketDataStatus failed:', err)
    throw new Error(`获取数据状态失败: ${err instanceof Error ? err.message : String(err)}`)
  }
}

// 获取当前版本信息
export async function getTierInfo(): Promise<TierInfo> {
  const App = getApp()

  if (!App || typeof App.GetTierInfo !== 'function') {
    throw new Error(BACKEND_UNAVAILABLE_MSG)
  }

  try {
    return await App.GetTierInfo()
  } catch (err) {
    console.error('GetTierInfo failed:', err)
    throw new Error(`获取版本信息失败: ${err instanceof Error ? err.message : String(err)}`)
  }
}

// 获取升级路径
export async function getUpgradePath(): Promise<UpgradePath> {
  const App = getApp()

  if (!App || typeof App.GetUpgradePath !== 'function') {
    throw new Error(BACKEND_UNAVAILABLE_MSG)
  }

  try {
    return await App.GetUpgradePath()
  } catch (err) {
    console.error('GetUpgradePath failed:', err)
    throw new Error(`获取升级信息失败: ${err instanceof Error ? err.message : String(err)}`)
  }
}
