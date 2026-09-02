import { useState, useEffect, useRef } from 'react'
import { BarChart3, Play, Calendar, TrendingUp, TrendingDown, Loader2, GitCompareArrows } from 'lucide-react'
import { useI18nStore } from '../store/i18nStore'
import {
  getStrategies,
  runBacktest,
  getBacktestResults,
  getBacktestStats,
  type Strategy,
  type BacktestResult,
} from '../services/strategy'
import {
  LineChart as ReLineChart,
  Line,
  AreaChart,
  Area,
  XAxis,
  YAxis,
  Tooltip,
  ResponsiveContainer,
  CartesianGrid,
} from 'recharts'

interface DisplayResult {
  strategy: string
  annualReturn: number
  sharpe: number
  maxDD: number
  winRate: number
  profitFactor: number
  totalTrades: number
  backtestPeriod: string
  initialCapital: number
  finalCapital: number
  startTime: string
  barsCount?: number
  durationMs?: number
  dataSource?: string
  stockCode?: string
  stockName?: string
  turnover?: number
  equityCurve?: number[]
  dates?: string[]
}

// 格式化浮点数，避免精度问题
const fmt = (v: number, decimals: number = 2): number => {
  if (isNaN(v) || !isFinite(v)) return 0
  const factor = Math.pow(10, decimals)
  return Math.round(v * factor) / factor
}

// ==== 回测诊断计算（均基于真实权益曲线与日期，无伪造） ====

// 月度收益矩阵：按月聚合权益曲线的区间收益
interface MonthCell { year: number; month: number; ret: number }
function buildMonthlyReturns(dates: string[], equity: number[]): MonthCell[] {
  if (!dates || !equity || dates.length === 0 || equity.length !== dates.length) return []
  const cells: MonthCell[] = []
  let prevMonth = ''
  let prevEq = equity[0] > 0 ? equity[0] : 1
  for (let i = 0; i < dates.length; i++) {
    const d = dates[i]
    const month = d.slice(0, 7)
    if (prevMonth === '') {
      prevMonth = month
      continue
    }
    if (month !== prevMonth) {
      const lastEq = equity[i - 1]
      cells.push(retCell(prevMonth, lastEq / prevEq - 1))
      prevMonth = month
      prevEq = lastEq > 0 ? lastEq : 1
    }
  }
  // 收尾：最后一个月到期末
  if (prevMonth !== '') {
    const lastEq = equity[equity.length - 1]
    cells.push(retCell(prevMonth, lastEq / prevEq - 1))
  }
  return cells
}

function retCell(monthKey: string, ret: number): MonthCell {
  const year = Number(monthKey.slice(0, 4))
  const month = Number(monthKey.slice(5, 7))
  return { year, month, ret: fmt(ret * 100, 2) }
}

// 水下曲线：累计回撤序列
function buildDrawdownCurve(dates: string[], equity: number[]): { date: string; dd: number }[] {
  const out: { date: string; dd: number }[] = []
  if (!dates || !equity || dates.length === 0) return out
  let peak = equity[0] > 0 ? equity[0] : 1
  for (let i = 0; i < equity.length; i++) {
    if (equity[i] > peak) peak = equity[i]
    const dd = peak > 0 ? -(peak - equity[i]) / peak * 100 : 0
    out.push({ date: dates[i].slice(0, 10), dd: fmt(dd, 2) })
  }
  return out
}

// 滚动夏普：滚动窗口（默认25根）年化夏普
function buildRollingSharpe(dates: string[], equity: number[], window = 25): { date: string; sharpe: number }[] {
  const out: { date: string; sharpe: number }[] = []
  if (!dates || !equity || equity.length < window + 1) return out
  for (let i = window; i < equity.length; i++) {
    const returns: number[] = []
    for (let j = i - window + 1; j <= i; j++) {
      if (equity[j - 1] > 0) returns.push((equity[j] - equity[j - 1]) / equity[j - 1])
    }
    if (returns.length < 2) continue
    const meanR = returns.reduce((a, b) => a + b, 0) / returns.length
    const sd = Math.sqrt(returns.reduce((a, b) => a + (b - meanR) * (b - meanR), 0) / (returns.length - 1))
    if (sd < 1e-8) continue
    out.push({ date: dates[i].slice(0, 10), sharpe: fmt(meanR / sd * Math.sqrt(252), 2) })
  }
  return out
}

// Calmar比率：总收益 / |最大回撤|（避免除零），仅用于横向对比。
// 注意：后端 max_drawdown 为正值（如33.79），此处统一取绝对值计算。
function calmar(totalReturn: number, maxDD: number): number {
  const dd = Math.abs(maxDD)
  if (dd < 1e-9) return 0
  return fmt(totalReturn / dd, 2)
}

interface StrategyConfig {
  strategy: string
  strategyId: string
  strategyType: string
}

export default function Backtest() {
  const t = useI18nStore((s) => s.t)
  const today = new Date().toISOString().split('T')[0]
  const oneYearAgo = new Date(Date.now() - 365 * 24 * 60 * 60 * 1000).toISOString().split('T')[0]

  const [strategies, setStrategies] = useState<Strategy[]>([])
  const [backtestStats, setBacktestStats] = useState<any>(null)

  const [config, setConfig] = useState({
    stockCode: '',
    stockName: '',
    market: '',
    period: 'day',
    startDate: oneYearAgo,
    endDate: today,
    initialCapital: '100000',
  })

  const [selectedStrategy, setSelectedStrategy] = useState<StrategyConfig | null>(null)

  const [results, setResults] = useState<DisplayResult[]>([])
  const [currentPage, setCurrentPage] = useState(1)
  const pageSize = 10
  const [comparePage, setComparePage] = useState(1)

  const [isRunning, setIsRunning] = useState(false)
  const [progress, setProgress] = useState(0)
  const [activeResult, setActiveResult] = useState<DisplayResult | null>(null)
  const [runMessage, setRunMessage] = useState('')

  // 加载策略列表和回测结果
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => {
    loadData()
    return () => {
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
    }
  }, [])

  const loadData = async () => {
    try {
      // 加载策略列表
      const strategyList = await getStrategies()
      setStrategies(strategyList)

      // 默认选择第一个策略
      if (strategyList.length > 0 && !selectedStrategy) {
        setSelectedStrategy({
          strategy: strategyList[0].name,
          strategyId: String(strategyList[0].id),
          strategyType: strategyList[0].strategy_type,
        })
      }

      // 加载回测结果
      const { results: historyResults } = await getBacktestResults('', 20)
      const displayResults: DisplayResult[] = historyResults.map((r: BacktestResult) => ({
        strategy: r.strategy_name,
        annualReturn: fmt(r.annual_return, 2),
        sharpe: fmt(r.sharpe_ratio, 4),
        maxDD: fmt(r.max_drawdown, 2),
        winRate: fmt(r.win_rate, 2),
        profitFactor: fmt(r.profit_factor, 2),
        totalTrades: r.total_trades,
        backtestPeriod: `${r.start_date} 至 ${r.end_date}`,
        initialCapital: r.initial_capital,
        finalCapital: fmt(r.final_capital, 2),
        startTime: r.created_at ? new Date(r.created_at).toLocaleString('zh-CN') : '-',
        barsCount: r.bars_count,
        durationMs: r.duration_ms,
        dataSource: '历史记录',
        stockCode: r.stock_code,
        stockName: r.stock_name,
      }))
      setResults(displayResults)

      // 加载回测统计
      const stats = await getBacktestStats()
      setBacktestStats(stats)
    } catch (err) {
      console.error('Failed to load data:', err)
      setRunMessage('加载数据失败，将30秒后自动重试，请检查后端连接')
      // 失败后 30 秒自动重试
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      retryTimerRef.current = setTimeout(loadData, 30000)
    }
  }

  const handleRun = async () => {
    if (!selectedStrategy) {
      setRunMessage('请选择一个策略')
      setTimeout(() => setRunMessage(''), 2000)
      return
    }

    if (!config.stockCode) {
      setRunMessage('请输入股票代码')
      setTimeout(() => setRunMessage(''), 2000)
      return
    }

    if (new Date(config.startDate) >= new Date(config.endDate)) {
      setRunMessage('开始日期必须早于结束日期')
      setTimeout(() => setRunMessage(''), 2000)
      return
    }

    setIsRunning(true)
    setProgress(0)

    try {
      setRunMessage(`正在执行回测: ${selectedStrategy.strategy}...`)
      setProgress(30)

      const result = await runBacktest({
        strategy_id: selectedStrategy.strategyId,
        strategy_name: selectedStrategy.strategy,
        strategy_type: selectedStrategy.strategyType,
        stock_code: config.stockCode,
        stock_name: config.stockName,
        market: config.market,
        period: config.period,
        start_date: config.startDate,
        end_date: config.endDate,
        initial_capital: Number(config.initialCapital),
      })

      if (result) {
        const durationSec = result.duration_ms ? (result.duration_ms / 1000).toFixed(3) : '0.000'
        const bars = result.bars_count || 0

        const displayResult: DisplayResult = {
          strategy: result.strategy_name,
          annualReturn: fmt(result.annual_return, 2),
          sharpe: fmt(result.sharpe_ratio, 4),
          maxDD: fmt(result.max_drawdown, 2),
          winRate: fmt(result.win_rate, 2),
          profitFactor: fmt(result.profit_factor, 2),
          totalTrades: result.total_trades,
          backtestPeriod: `${result.start_date} 至 ${result.end_date}`,
          initialCapital: result.initial_capital,
          finalCapital: fmt(result.final_capital, 2),
          startTime: new Date(result.created_at).toLocaleString('zh-CN'),
          barsCount: bars,
          durationMs: result.duration_ms,
          dataSource: 'DuckDB/TDX 实时行情',
          stockCode: result.stock_code,
          stockName: result.stock_name,
          turnover: result.turnover,
          equityCurve: result.equity_curve || [],
          dates: result.dates || [],
        }

        setResults([displayResult, ...results.filter(r =>
          !(r.strategy === displayResult.strategy && r.startTime === displayResult.startTime)
        )])
        setActiveResult(displayResult)
        setProgress(100)
        setRunMessage(`✓ 回测完成，耗时 ${durationSec}秒`)
        setTimeout(() => setRunMessage(''), 5000)
      } else {
        setProgress(0)
        setRunMessage('回测执行失败')
        setTimeout(() => setRunMessage(''), 3000)
      }
    } catch (e) {
      setProgress(0)
      const errorMsg = e instanceof Error ? e.message : String(e)
      setRunMessage(`回测失败: ${errorMsg}`)
      setTimeout(() => setRunMessage(''), 5000)
    } finally {
      setIsRunning(false)
    }
  }

  // 最近结果分页计算
  const totalPages = Math.ceil(results.length / pageSize) || 1
  const startIndex = (currentPage - 1) * pageSize
  const paginatedResults = results.slice(startIndex, startIndex + pageSize)

  const handlePageChange = (page: number) => {
    if (page >= 1 && page <= totalPages) {
      setCurrentPage(page)
    }
  }

  // 多策略横向对比分页计算
  const compareTotalPages = Math.ceil(strategies.length / pageSize) || 1
  const compareStartIndex = (comparePage - 1) * pageSize
  const paginatedStrategies = strategies.slice(compareStartIndex, compareStartIndex + pageSize)

  const handleComparePageChange = (page: number) => {
    if (page >= 1 && page <= compareTotalPages) {
      setComparePage(page)
    }
  }

  // 数据量变化时，若当前页超出范围则回到第1页
  useEffect(() => {
    if (comparePage > compareTotalPages) setComparePage(1)
  }, [compareTotalPages, comparePage])
  useEffect(() => {
    if (currentPage > totalPages) setCurrentPage(1)
  }, [totalPages, currentPage])

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-purple-500 to-indigo-500 flex items-center justify-center">
            <BarChart3 className="w-5 h-5 text-white" />
          </div>
          <h1 className="text-2xl font-bold text-slate-800 dark:text-slate-100">{t('backtest.title')}</h1>
        </div>
        <div className="flex items-center gap-2">
          {backtestStats && (
            <div className="text-xs text-slate-500 dark:text-slate-400 mr-2">
              累计回测: {backtestStats.total_runs} 次 | 
              平均收益: {backtestStats.avg_return?.toFixed(2)}%
            </div>
          )}
          <button
            className="btn-primary flex items-center gap-2"
            onClick={handleRun}
            disabled={isRunning || strategies.length === 0}
          >
            {isRunning ? (
              <>
                <Loader2 className="w-4 h-4 animate-spin" />
                回测中...
              </>
            ) : (
              <>
                <Play className="w-4 h-4" />
                {t('backtest.runBacktest')}
              </>
            )}
          </button>
        </div>
      </div>

      {runMessage && (
        <div className={`card p-3 ${runMessage.includes('失败') || runMessage.includes('错误') ? 'bg-red-50 dark:bg-red-900/20 border-red-200 dark:border-red-800' : 'bg-green-50 dark:bg-green-900/20 border-green-200 dark:border-green-800'}`}>
          <div className="flex items-center gap-2">
            {runMessage.includes('失败') || runMessage.includes('错误') ? (
              <TrendingDown className="w-4 h-4 text-red-500" />
            ) : (
              <TrendingUp className="w-4 h-4 text-green-500" />
            )}
            <span className={`text-sm ${runMessage.includes('失败') || runMessage.includes('错误') ? 'text-red-700 dark:text-red-400' : 'text-green-700 dark:text-green-400'}`}>
              {runMessage}
            </span>
          </div>
        </div>
      )}

      {/* Backtest Config Card */}
      <div className="card p-5">
        <div className="flex items-center gap-3 mb-4">
          <Calendar className="w-5 h-5 text-brand-500" />
          <h3 className="font-medium text-slate-800 dark:text-slate-100">{t('backtest.configTitle')}</h3>
        </div>
        <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
          <div>
            <label className="text-xs text-slate-500 dark:text-slate-400 mb-1 block">股票代码</label>
            <input
              type="text"
              className="input-field"
              value={config.stockCode}
              onChange={(e) => setConfig({ ...config, stockCode: e.target.value })}
              disabled={isRunning}
              placeholder="如 600519"
            />
          </div>
          <div>
            <label className="text-xs text-slate-500 dark:text-slate-400 mb-1 block">市场</label>
            <select
              className="input-field"
              value={config.market}
              onChange={(e) => setConfig({ ...config, market: e.target.value })}
              disabled={isRunning}
            >
              <option value="SH">上海 (SH)</option>
              <option value="SZ">深圳 (SZ)</option>
              <option value="BJ">北京 (BJ)</option>
            </select>
          </div>
          <div>
            <label className="text-xs text-slate-500 dark:text-slate-400 mb-1 block">周期</label>
            <select
              className="input-field"
              value={config.period}
              onChange={(e) => setConfig({ ...config, period: e.target.value })}
              disabled={isRunning}
            >
              <option value="day">日线</option>
              <option value="week">周线</option>
              <option value="month">月线</option>
              <option value="5min">5分钟</option>
              <option value="15min">15分钟</option>
              <option value="30min">30分钟</option>
              <option value="60min">60分钟</option>
            </select>
          </div>
          <div>
            <label className="text-xs text-slate-500 dark:text-slate-400 mb-1 block">
              策略选择
            </label>
            <select
              className="input-field"
              value={selectedStrategy?.strategy || ''}
              onChange={(e) => {
                const strategy = strategies.find(s => s.name === e.target.value)
                if (strategy) {
                  setSelectedStrategy({
                    strategy: strategy.name,
                    strategyId: String(strategy.id),
                    strategyType: strategy.strategy_type,
                  })
                }
              }}
              disabled={isRunning || strategies.length === 0}
            >
              <option value="">-- 选择策略 --</option>
              {strategies.map(s => (
                <option key={s.id} value={s.name}>
                  {s.name} ({s.status === 'running' ? '运行中' : '已停止'})
                </option>
              ))}
            </select>
          </div>
        </div>
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4 mt-4">
          <div>
            <label className="text-xs text-slate-500 dark:text-slate-400 mb-1 block">{t('backtest.startDate')}</label>
            <input
              type="date"
              className="input-field cursor-pointer"
              value={config.startDate}
              max={config.endDate}
              onChange={(e) => setConfig({ ...config, startDate: e.target.value })}
              disabled={isRunning}
            />
          </div>
          <div>
            <label className="text-xs text-slate-500 dark:text-slate-400 mb-1 block">{t('backtest.endDate')}</label>
            <input
              type="date"
              className="input-field cursor-pointer"
              value={config.endDate}
              min={config.startDate}
              max={today}
              onChange={(e) => setConfig({ ...config, endDate: e.target.value })}
              disabled={isRunning}
            />
          </div>
          <div>
            <label className="text-xs text-slate-500 dark:text-slate-400 mb-1 block">{t('backtest.initialCapital')}</label>
            <input
              type="number"
              className="input-field"
              value={config.initialCapital}
              onChange={(e) => setConfig({ ...config, initialCapital: e.target.value })}
              disabled={isRunning}
            />
          </div>
        </div>
        <div className="mt-3 flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400 flex-wrap">
          <span>标的：{config.market}{config.stockCode} ({config.stockName || '未命名'})</span>
          <span className="text-slate-300 dark:text-slate-600">|</span>
          <span>周期：{config.period}</span>
          <span className="text-slate-300 dark:text-slate-600">|</span>
          <span>回测区间：{config.startDate} 至 {config.endDate}</span>
          <span className="text-slate-300 dark:text-slate-600">|</span>
          <span>初始资金：¥{Number(config.initialCapital).toLocaleString()}</span>
          <span className="text-slate-300 dark:text-slate-600">|</span>
          <span>策略：{selectedStrategy?.strategy || '未选择'}</span>
        </div>
        
        {isRunning && (
          <div className="mt-4">
            <div className="flex items-center justify-between text-xs text-slate-500 dark:text-slate-400 mb-1">
              <span>回测进度</span>
              <span>{Math.round(progress)}%</span>
            </div>
            <div className="w-full bg-slate-200 dark:bg-slate-700 rounded-full h-2 overflow-hidden">
              <div 
                className="bg-brand-500 h-full rounded-full transition-all duration-200 ease-out"
                style={{ width: `${Math.min(progress, 100)}%` }}
              />
            </div>
          </div>
        )}
      </div>

      {/* Active Result Card */}
      {activeResult && (
        <div className="card p-5 border-2 border-brand-200 dark:border-brand-800 bg-brand-50/30 dark:bg-brand-900/10">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-3">
              <BarChart3 className="w-5 h-5 text-brand-500" />
              <h3 className="font-medium text-slate-800 dark:text-slate-100">最新回测结果</h3>
              <span className="text-xs text-slate-500 dark:text-slate-400">{activeResult.startTime}</span>
            </div>
            <button
              onClick={() => setActiveResult(null)}
              className="text-slate-400 hover:text-slate-600 dark:hover:text-slate-300 text-sm"
            >
              关闭详情
            </button>
          </div>
          
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
            <div className="p-3 bg-white dark:bg-slate-800 rounded-lg border border-slate-100 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">策略</div>
              <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">{activeResult.strategy}</div>
            </div>
            <div className="p-3 bg-white dark:bg-slate-800 rounded-lg border border-slate-100 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">年化收益</div>
              <div className={`text-lg font-semibold ${activeResult.annualReturn >= 0 ? 'text-green-500' : 'text-red-500'}`}>
                {activeResult.annualReturn >= 0 ? '+' : ''}{activeResult.annualReturn}%
              </div>
            </div>
            <div className="p-3 bg-white dark:bg-slate-800 rounded-lg border border-slate-100 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">夏普比率</div>
              <div className="text-lg font-semibold text-slate-800 dark:text-slate-100">{activeResult.sharpe}</div>
            </div>
            <div className="p-3 bg-white dark:bg-slate-800 rounded-lg border border-slate-100 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">最大回撤</div>
              <div className="text-lg font-semibold text-red-500">{activeResult.maxDD}%</div>
            </div>
            <div className="p-3 bg-white dark:bg-slate-800 rounded-lg border border-slate-100 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">胜率</div>
              <div className="text-lg font-semibold text-slate-800 dark:text-slate-100">{activeResult.winRate}%</div>
            </div>
            <div className="p-3 bg-white dark:bg-slate-800 rounded-lg border border-slate-100 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">盈亏比</div>
              <div className="text-lg font-semibold text-slate-800 dark:text-slate-100">{activeResult.profitFactor}</div>
            </div>
            <div className="p-3 bg-white dark:bg-slate-800 rounded-lg border border-slate-100 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">交易次数</div>
              <div className="text-lg font-semibold text-slate-800 dark:text-slate-100">{activeResult.totalTrades}</div>
            </div>
            <div className="p-3 bg-white dark:bg-slate-800 rounded-lg border border-slate-100 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">期末资金</div>
              <div className="text-lg font-semibold text-green-500">¥{activeResult.finalCapital.toLocaleString()}</div>
            </div>
          </div>
          
          <div className="mt-4 text-xs text-slate-500 dark:text-slate-400">
            回测区间：{activeResult.backtestPeriod} | 初始资金：¥{activeResult.initialCapital.toLocaleString()}
            {activeResult.barsCount != null && (
              <> | K线数量：{activeResult.barsCount} 条</>
            )}
            {activeResult.durationMs != null && (
              <> | 计算耗时：{(activeResult.durationMs / 1000).toFixed(3)}秒</>
            )}
            {activeResult.dataSource && (
              <> | 数据源：{activeResult.dataSource}</>
            )}
          </div>
          
          <div className="mt-3 pt-3 border-t border-slate-200 dark:border-slate-700">
            <div className="text-xs text-slate-400 dark:text-slate-500 mb-2 flex items-center gap-1">
              <span className="inline-block w-2 h-2 rounded-full bg-green-500"></span>
              数据真实性验证
            </div>
            <div className="grid grid-cols-2 md:grid-cols-4 gap-2 text-xs text-slate-600 dark:text-slate-400">
              <div>标的：{activeResult.stockCode || 'N/A'}</div>
              <div>名称：{activeResult.stockName || 'N/A'}</div>
              <div>周期：{config.period}</div>
              <div>交易制度：T+1 · 1手=100股</div>
            </div>
          </div>

          {activeResult.equityCurve && activeResult.dates && activeResult.equityCurve.length > 1 && (
            <BacktestCharts equityCurve={activeResult.equityCurve} dates={activeResult.dates} />
          )}
        </div>
      )}

      {/* 多策略横向对比看板 */}
      <div className="card overflow-hidden">
        <div className="p-5 border-b border-slate-200 dark:border-slate-700 flex items-center justify-between">
          <div className="flex items-center gap-2">
            <GitCompareArrows className="w-5 h-5 text-brand-500" />
            <h3 className="font-medium text-slate-800 dark:text-slate-100">多策略横向对比</h3>
          </div>
          <span className="text-xs text-slate-500 dark:text-slate-400">
            Sharpe / 卡玛比率 / 回撤 / 换手 / 胜率（指标来自真实回测刷新）
          </span>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="text-left text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-700">
                <th className="px-5 py-3 font-medium">策略</th>
                <th className="px-5 py-3 font-medium">Sharpe</th>
                <th className="px-5 py-3 font-medium">卡玛比率</th>
                <th className="px-5 py-3 font-medium">最大回撤</th>
                <th className="px-5 py-3 font-medium">年化换手</th>
                <th className="px-5 py-3 font-medium">胜率</th>
              </tr>
            </thead>
            <tbody>
              {strategies.length === 0 ? (
                <tr>
                  <td colSpan={6} className="text-center py-8 text-slate-400 dark:text-slate-500">
                    暂无策略数据，请先创建策略（指标将在启动/每日调优后用真实回测刷新）
                  </td>
                </tr>
              ) : (
                paginatedStrategies.map((s) => (
                  <tr key={s.id} className="border-b border-slate-100 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800/50 transition-colors">
                    <td className="px-5 py-4">
                      <div className="font-medium text-slate-800 dark:text-slate-100">{s.name}</div>
                      <div className="text-xs text-slate-400 dark:text-slate-500 mt-0.5">{s.strategy_type}</div>
                    </td>
                    <td className={`px-5 py-4 font-medium ${s.sharpe_ratio >= 0 ? 'text-green-500' : 'text-red-500'}`}>{fmt(s.sharpe_ratio, 2)}</td>
                    <td className="px-5 py-4 font-medium text-slate-800 dark:text-slate-100">{calmar(s.total_return, s.max_drawdown)}</td>
                    <td className="px-5 py-4 font-medium text-red-500">{fmt(s.max_drawdown, 2)}%</td>
                    <td className="px-5 py-4 text-slate-800 dark:text-slate-100">{s.turnover ? `${fmt(s.turnover, 1)}x` : '—'}</td>
                    <td className="px-5 py-4 text-slate-800 dark:text-slate-100">{fmt(s.win_rate, 1)}%</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        {/* 多策略对比分页导航 */}
        {strategies.length > pageSize && (
          <PaginationBar
            page={comparePage}
            totalPages={compareTotalPages}
            total={strategies.length}
            startIndex={compareStartIndex}
            pageSize={pageSize}
            onChange={handleComparePageChange}
          />
        )}
      </div>

      {/* Results Table */}
      <div className="card overflow-hidden">
        <div className="p-5 border-b border-slate-200 dark:border-slate-700 flex items-center justify-between">
          <h3 className="font-medium text-slate-800 dark:text-slate-100">{t('backtest.recentResults')}</h3>
          <span className="text-xs text-slate-500 dark:text-slate-400">共 {results.length} 条记录 | 每页 {pageSize} 条</span>
        </div>
        <div className="overflow-x-auto">
          <table className="w-full">
            <thead>
              <tr className="text-left text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-700">
                <th className="px-5 py-3 font-medium">序号</th>
                <th className="px-5 py-3 font-medium">{t('backtest.strategy')}</th>
                <th className="px-5 py-3 font-medium">{t('backtest.annualReturn')}</th>
                <th className="px-5 py-3 font-medium">{t('backtest.sharpe')}</th>
                <th className="px-5 py-3 font-medium">{t('backtest.maxDrawdown')}</th>
                <th className="px-5 py-3 font-medium">{t('backtest.winRate')}</th>
                <th className="px-5 py-3 font-medium">交易次数</th>
                <th className="px-5 py-3 font-medium">回测时间</th>
                <th className="px-5 py-3 font-medium">操作</th>
              </tr>
            </thead>
            <tbody>
              {results.length === 0 ? (
                <tr>
                  <td colSpan={9} className="text-center py-8 text-slate-400 dark:text-slate-500">
                    暂无回测结果，请点击「运行回测」开始
                  </td>
                </tr>
              ) : (
                paginatedResults.map((r, idx) => (
                  <tr 
                    key={`${r.strategy}-${r.startTime}-${idx}`} 
                    className={`border-b border-slate-100 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800/50 transition-colors cursor-pointer ${activeResult?.strategy === r.strategy && activeResult?.startTime === r.startTime ? 'bg-brand-50 dark:bg-brand-900/20' : ''}`}
                    onClick={() => setActiveResult(r)}
                  >
                    <td className="px-5 py-4 text-slate-500 dark:text-slate-400 text-sm">
                      {startIndex + idx + 1}
                    </td>
                    <td className="px-5 py-4">
                      <div className="flex items-center gap-2">
                        <BarChart3 className="w-4 h-4 text-brand-500" />
                        <span className="font-medium text-slate-800 dark:text-slate-100">{r.strategy}</span>
                      </div>
                    </td>
                    <td className={`px-5 py-4 font-medium ${r.annualReturn >= 0 ? 'text-green-500' : 'text-red-500'}`}>
                      {r.annualReturn >= 0 ? '+' : ''}{r.annualReturn}%
                    </td>
                    <td className="px-5 py-4 text-slate-800 dark:text-slate-100 font-medium">{r.sharpe}</td>
                    <td className="px-5 py-4 text-red-500 font-medium">{r.maxDD}%</td>
                    <td className="px-5 py-4 text-slate-800 dark:text-slate-100">{r.winRate}%</td>
                    <td className="px-5 py-4 text-slate-600 dark:text-slate-400">{r.totalTrades}</td>
                    <td className="px-5 py-4 text-xs text-slate-500 dark:text-slate-400">{r.startTime}</td>
                    <td className="px-5 py-4">
                      <button 
                        onClick={(e) => {
                          e.stopPropagation()
                          setActiveResult(r)
                        }}
                        className="text-brand-500 hover:text-brand-600 text-xs font-medium"
                      >
                        查看详情
                      </button>
                    </td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
        
        {/* 分页导航 */}
        {results.length > pageSize && (
          <PaginationBar
            page={currentPage}
            totalPages={totalPages}
            total={results.length}
            startIndex={startIndex}
            pageSize={pageSize}
            onChange={handlePageChange}
          />
        )}
      </div>
    </div>
  )
}

// ==================== 子组件 ====================

// 通用分页栏：显示当前区间 + 上一页/页码/下一页
function PaginationBar({
  page,
  totalPages,
  total,
  startIndex,
  pageSize,
  onChange,
}: {
  page: number
  totalPages: number
  total: number
  startIndex: number
  pageSize: number
  onChange: (page: number) => void
}) {
  const pageNumbers = () => {
    const pages: (number | string)[] = []
    const maxVisible = 5
    let startPage = Math.max(1, page - Math.floor(maxVisible / 2))
    let endPage = Math.min(totalPages, startPage + maxVisible - 1)
    if (endPage - startPage + 1 < maxVisible) {
      startPage = Math.max(1, endPage - maxVisible + 1)
    }
    for (let i = startPage; i <= endPage; i++) pages.push(i)
    if (startPage > 1) {
      pages.unshift('...')
      pages.unshift(1)
    }
    if (endPage < totalPages) {
      pages.push('...')
      pages.push(totalPages)
    }
    return pages
  }
  return (
    <div className="px-5 py-4 border-t border-slate-200 dark:border-slate-700 flex items-center justify-between bg-slate-50 dark:bg-slate-800/50">
      <div className="text-xs text-slate-500 dark:text-slate-400">
        显示第 {startIndex + 1} - {Math.min(startIndex + pageSize, total)} 条，共 {total} 条
      </div>
      <div className="flex items-center gap-1">
        <button
          onClick={() => onChange(page - 1)}
          disabled={page === 1}
          className="px-3 py-1 text-xs rounded-md border border-slate-300 dark:border-slate-600 text-slate-600 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          上一页
        </button>
        {pageNumbers().map((p, idx) => (
          <span key={`page-${idx}`}>
            {p === '...' ? (
              <span className="px-2 py-1 text-xs text-slate-400">...</span>
            ) : (
              <button
                onClick={() => onChange(p as number)}
                className={`px-3 py-1 text-xs rounded-md transition-colors ${
                  page === p
                    ? 'bg-brand-500 text-white'
                    : 'border border-slate-300 dark:border-slate-600 text-slate-600 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700'
                }`}
              >
                {p}
              </button>
            )}
          </span>
        ))}
        <button
          onClick={() => onChange(page + 1)}
          disabled={page === totalPages}
          className="px-3 py-1 text-xs rounded-md border border-slate-300 dark:border-slate-600 text-slate-600 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 disabled:opacity-50 disabled:cursor-not-allowed transition-colors"
        >
          下一页
        </button>
      </div>
    </div>
  )
}

function BacktestCharts({ equityCurve, dates }: { equityCurve: number[]; dates: string[] }) {
  const monthly = buildMonthlyReturns(dates, equityCurve)
  const ddCurve = buildDrawdownCurve(dates, equityCurve)
  const rollingSharpe = buildRollingSharpe(dates, equityCurve)

  // 月度热力图：按 年 × 月 汇总
  const years = Array.from(new Set(monthly.map((m) => m.year))).sort()
  const monthlyMap = new Map<string, number>()
  monthly.forEach((m) => monthlyMap.set(`${m.year}-${m.month}`, m.ret))

  const heatColor = (ret: number): string => {
    if (ret >= 3) return '#16a34a'
    if (ret >= 1) return '#4ade80'
    if (ret >= 0) return '#86efac'
    if (ret >= -1) return '#fca5a5'
    if (ret >= -3) return '#ef4444'
    return '#b91c1c'
  }

  return (
    <div className="mt-4 pt-3 border-t border-slate-200 dark:border-slate-700 space-y-4">
      {/* 月度收益热力图 */}
      <div>
        <div className="text-xs text-slate-400 dark:text-slate-500 mb-2">月度收益热力图（%）</div>
        <div className="overflow-x-auto">
          <table className="text-xs min-w-full">
            <thead>
              <tr>
                <th className="px-2 py-1 text-left text-slate-400 font-medium">年</th>
                {Array.from({ length: 12 }, (_, i) => (
                  <th key={i} className="px-1.5 py-1 text-center text-slate-400 font-medium">{i + 1}月</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {years.map((y) => (
                <tr key={y}>
                  <td className="px-2 py-1 text-slate-500">{y}</td>
                  {Array.from({ length: 12 }, (_, i) => {
                    const ret = monthlyMap.get(`${y}-${i + 1}`)
                    return ret === undefined ? (
                      <td key={i} className="px-1.5 py-1 text-center text-slate-400 dark:text-slate-600">—</td>
                    ) : (
                      <td
                        key={i}
                        className="px-1.5 py-1 text-center font-medium text-white rounded"
                        style={{ backgroundColor: heatColor(ret) }}
                        title={`${y}年${i + 1}月 ${ret}%`}
                      >
                        {ret}
                      </td>
                    )
                  })}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      {/* 水下曲线 */}
      <div>
        <div className="text-xs text-slate-400 dark:text-slate-500 mb-2">资金水下曲线（Cumulative Drawdown）</div>
        <div className="h-48">
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={ddCurve} margin={{ top: 5, right: 10, left: 0, bottom: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#64748b33" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} minTickGap={40} />
              <YAxis tick={{ fontSize: 10 }} />
              <Tooltip formatter={(v) => [`${v}%`, '回撤']} />
              <Area type="monotone" dataKey="dd" stroke="#ef4444" fill="#ef444444" name="回撤" />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      </div>

      {/* 滚动夏普 */}
      <div>
        <div className="text-xs text-slate-400 dark:text-slate-500 mb-2">滚动夏普比率（窗口25根K线，年化）</div>
        <div className="h-48">
          <ResponsiveContainer width="100%" height="100%">
            <ReLineChart data={rollingSharpe} margin={{ top: 5, right: 10, left: 0, bottom: 0 }}>
              <CartesianGrid strokeDasharray="3 3" stroke="#64748b33" />
              <XAxis dataKey="date" tick={{ fontSize: 10 }} minTickGap={40} />
              <YAxis tick={{ fontSize: 10 }} />
              <Tooltip formatter={(v) => [String(v), '夏普']} />
              <Line type="monotone" dataKey="sharpe" stroke="#6366f1" dot={false} name="夏普" />
            </ReLineChart>
          </ResponsiveContainer>
        </div>
      </div>
    </div>
  )
}
