// 策略与回测 API 服务
import { getAppInstance } from './api'

export interface Strategy {
  id: number
  name: string
  description: string
  strategy_type: string
  status: string
  is_builtin: boolean
  sharpe_ratio: number
  max_drawdown: number
  total_return: number
  win_rate: number
  turnover: number
  initial_capital: number
  max_position: number
  stop_loss_pct: number
  take_profit_pct: number
  created_at: string
  updated_at: string
}

export interface BacktestResult {
  id: number
  strategy_id: string
  strategy_name: string
  strategy_type: string
  stock_code: string
  stock_name: string
  market: string
  period: string
  start_date: string
  end_date: string
  initial_capital: number
  final_capital: number
  annual_return: number
  sharpe_ratio: number
  max_drawdown: number
  win_rate: number
  profit_factor: number
  total_trades: number
  win_trades: number
  loss_trades: number
  bars_count: number
  turnover: number
  status: string
  error_message: string
  run_time: string
  duration_ms: number
  created_at: string
  equity_curve?: number[]
  dates?: string[]
}

export interface BacktestStats {
  total_runs: number
  completed_runs: number
  failed_runs: number
  avg_return: number
  best_return: number
}

interface StrategyListResponse {
  strategies: Strategy[]
}

interface DeleteResponse {
  status: string
}

interface BacktestResultsResponse {
  results: BacktestResult[]
  totalCount: number
}

// 获取策略列表
export async function getStrategies(status?: string): Promise<Strategy[]> {
  const app = getAppInstance()
  if (!app) {
    return []
  }

  try {
    const result = (await app.GetStrategies(status || '')) as StrategyListResponse
    return result?.strategies || []
  } catch (err) {
    console.error('GetStrategies failed:', err)
    return []
  }
}

// 后台异步重新回测全部内置策略并写入最新指标（触发真实回测，而非仅重读旧值）
export async function refreshStrategyMetrics(): Promise<{ started: boolean; message: string }> {
  const app = getAppInstance()
  if (!app) {
    return { started: false, message: '后端未就绪' }
  }
  try {
    return (await app.RefreshStrategyMetrics()) as { started: boolean; message: string }
  } catch (err) {
    console.error('RefreshStrategyMetrics failed:', err)
    return { started: false, message: String(err) }
  }
}

// 获取单个策略
export async function getStrategy(id: number): Promise<Strategy | null> {
  const app = getAppInstance()
  if (!app) {
    return null
  }

  try {
    return (await app.GetStrategy(id)) as Strategy | null
  } catch (err) {
    console.error('GetStrategy failed:', err)
    return null
  }
}

// 创建策略
export async function createStrategy(params: {
  name: string
  description: string
  strategy_type: string
  initial_capital: number
  max_position: number
  stop_loss_pct: number
  take_profit_pct: number
  config_json?: string
}): Promise<Strategy | null> {
  const app = getAppInstance()
  if (!app) {
    return null
  }

  try {
    return (await app.CreateStrategy(
      params.name,
      params.description,
      params.strategy_type,
      params.initial_capital,
      params.max_position,
      params.stop_loss_pct,
      params.take_profit_pct,
      'user',
      '用户',
      params.config_json || '{}'
    )) as Strategy | null
  } catch (err) {
    console.error('CreateStrategy failed:', err)
    return null
  }
}

// 更新策略
export async function updateStrategy(id: number, params: {
  name: string
  description: string
  strategy_type: string
  initial_capital: number
  max_position: number
  stop_loss_pct: number
  take_profit_pct: number
}): Promise<Strategy | null> {
  const app = getAppInstance()
  if (!app) {
    return null
  }

  try {
    return (await app.UpdateStrategy(
      id,
      params.name,
      params.description,
      params.strategy_type,
      params.initial_capital,
      params.max_position,
      params.stop_loss_pct,
      params.take_profit_pct
    )) as Strategy | null
  } catch (err) {
    console.error('UpdateStrategy failed:', err)
    return null
  }
}

// 删除策略
export async function deleteStrategy(id: number): Promise<boolean> {
  const app = getAppInstance()
  if (!app) {
    return false
  }

  try {
    const result = (await app.DeleteStrategy(id)) as DeleteResponse
    return result?.status === 'ok'
  } catch (err) {
    console.error('DeleteStrategy failed:', err)
    return false
  }
}

// 切换策略状态
export async function toggleStrategyStatus(id: number): Promise<Strategy | null> {
  const app = getAppInstance()
  if (!app) {
    return null
  }

  try {
    return (await app.ToggleStrategyStatus(id)) as Strategy | null
  } catch (err) {
    console.error('ToggleStrategyStatus failed:', err)
    return null
  }
}

// 运行回测
export async function runBacktest(params: {
  strategy_id: string
  strategy_name: string
  strategy_type: string
  start_date: string
  end_date: string
  initial_capital: number
  stock_code: string
  stock_name: string
  market: string
  period: string
}): Promise<BacktestResult | null> {
  const app = getAppInstance()
  if (!app) {
    return null
  }

  try {
    return (await app.RunBacktest(
      params.strategy_id,
      params.strategy_name,
      params.strategy_type,
      params.start_date,
      params.end_date,
      params.initial_capital,
      'user',
      '用户',
      params.stock_code || '',
      params.stock_name || '',
      params.market || 'SH',
      params.period || 'day'
    )) as BacktestResult | null
  } catch (err) {
    console.error('RunBacktest failed:', err)
    return null
  }
}

// 获取回测结果列表
export async function getBacktestResults(strategyId?: string, limit: number = 20): Promise<{ results: BacktestResult[], totalCount: number }> {
  const app = getAppInstance()
  if (!app) {
    return { results: [], totalCount: 0 }
  }

  try {
    const result = (await app.GetBacktestResults(strategyId || '', '', limit, 0)) as BacktestResultsResponse
    return {
      results: result?.results || [],
      totalCount: result?.totalCount || 0,
    }
  } catch (err) {
    console.error('GetBacktestResults failed:', err)
    return { results: [], totalCount: 0 }
  }
}

// 获取回测统计信息
export async function getBacktestStats(): Promise<BacktestStats | null> {
  const app = getAppInstance()
  if (!app) {
    return null
  }

  try {
    return (await app.GetBacktestStats()) as BacktestStats | null
  } catch (err) {
    console.error('GetBacktestStats failed:', err)
    return null
  }
}

// 删除回测结果
export async function deleteBacktestResult(id: number): Promise<boolean> {
  const app = getAppInstance()
  if (!app) {
    return false
  }

  try {
    const result = (await app.DeleteBacktestResult(id)) as DeleteResponse
    return result?.status === 'ok'
  } catch (err) {
    console.error('DeleteBacktestResult failed:', err)
    return false
  }
}
