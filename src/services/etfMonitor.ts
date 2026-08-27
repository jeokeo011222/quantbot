// ETF监控 API 服务

export interface ETFIndicator {
  key: string
  name: string
  group: string
  state: string // red / green / gray
  value: number
  display: string
  note: string
  reason: string
  date: string
}

export interface ETFMonitorResult {
  code: string
  name: string
  market: string
  index: string
  date: string
  price: number
  change_pct: number
  indicators: ETFIndicator[]
  red_count: number
  green_count: number
  gray_count: number
  total: number
  verdict: string
}

export interface MarketSentiment {
  date: string
  turnover_value: number
  turnover_percentile: number
  margin_value: number
  margin_percentile: number
  turnover_state: string
  margin_state: string
}

export interface ETFMonitorResponse {
  results: ETFMonitorResult[]
  market: MarketSentiment
  generated_at: string
  source: string
}

export interface ETFKlinePoint {
  date: string
  open: number
  high: number
  low: number
  close: number
  volume: number
}

export interface ETFKlineResponse {
  code: string
  name: string
  market: string
  index: string
  bars: ETFKlinePoint[]
}

const BACKEND_UNAVAILABLE_MSG = 'QuantBot 后端服务未启动，请重启应用后重试'

function getApp(): any {
  return (window as any)['go']?.['main']?.['App']
}

// 获取ETF监控数据（手动刷新）
export async function getETFMonitorData(): Promise<ETFMonitorResponse> {
  const App = getApp()

  if (!App || typeof App.GetETFMonitorData !== 'function') {
    throw new Error(BACKEND_UNAVAILABLE_MSG)
  }

  try {
    const result = await App.GetETFMonitorData()
    return result
  } catch (err) {
    console.error('GetETFMonitorData failed:', err)
    throw new Error(`ETF监控数据获取失败: ${err instanceof Error ? err.message : String(err)}`)
  }
}

// 获取单只ETF近3个月K线
export async function getETFMonitorKline(code: string): Promise<ETFKlineResponse> {
  const App = getApp()

  if (!App || typeof App.GetETFMonitorKline !== 'function') {
    throw new Error(BACKEND_UNAVAILABLE_MSG)
  }

  try {
    const result = await App.GetETFMonitorKline(code)
    return result
  } catch (err) {
    console.error('GetETFMonitorKline failed:', err)
    throw new Error(`ETF K线获取失败: ${err instanceof Error ? err.message : String(err)}`)
  }
}
