import { useState, useEffect, useMemo, useRef } from 'react'
import {
  Search,
  TrendingUp,
  TrendingDown,
  Shield,
  BarChart3,
  ChevronRight,
  ChevronLeft,
  Sparkles,
  Eye,
  RefreshCw,
  X,
} from 'lucide-react'
import {
  screenStock,
  getStrategyList,
  getMarketDataStatus,
  type ScreeningResult,
  type StrategyTemplate,
  type StockScore,
  type FactorScore,
  type MarketDataStatus,
} from '../services/screener'
import { toFriendlyError } from '../utils/errorMessages'
import { useToastStore } from '../store/toastStore'

const FACTOR_COLORS: Record<string, { bg: string; text: string; bar: string }> = {
  value: { bg: 'bg-blue-50 dark:bg-blue-900/20', text: 'text-blue-600 dark:text-blue-400', bar: 'bg-blue-500' },
  quality: { bg: 'bg-green-50 dark:bg-green-900/20', text: 'text-green-600 dark:text-green-400', bar: 'bg-green-500' },
  momentum: { bg: 'bg-orange-50 dark:bg-orange-900/20', text: 'text-orange-600 dark:text-orange-400', bar: 'bg-orange-500' },
  low_volatility: { bg: 'bg-purple-50 dark:bg-purple-900/20', text: 'text-purple-600 dark:text-purple-400', bar: 'bg-purple-500' },
  earnings_stability: { bg: 'bg-pink-50 dark:bg-pink-900/20', text: 'text-pink-600 dark:text-pink-400', bar: 'bg-pink-500' },
  liquidity: { bg: 'bg-cyan-50 dark:bg-cyan-900/20', text: 'text-cyan-600 dark:text-cyan-400', bar: 'bg-cyan-500' },
}

const FACTOR_NAMES: Record<string, string> = {
  value: '价值',
  quality: '质量',
  momentum: '动量',
  low_volatility: '低波动',
  earnings_stability: '盈利稳定',
  liquidity: '流动性',
}

const FACTOR_ORDER = ['value', 'quality', 'momentum', 'low_volatility', 'earnings_stability', 'liquidity']

function buildFormulaString(weights: Record<string, number>): string {
  if (!weights) return ''
  const parts: string[] = []
  for (const id of FACTOR_ORDER) {
    const w = weights[id]
    if (w !== undefined && w > 0) {
      parts.push(`${Math.round(w * 100)}%×${FACTOR_NAMES[id] || id}`)
    }
  }
  return `Score = ${parts.join(' + ')}`
}

const STRATEGY_ICONS: Record<string, typeof Shield> = {
  balanced: Shield,
  defensive: Sparkles,
  growth: TrendingUp,
}

export default function StockScreener() {
  const [strategies, setStrategies] = useState<StrategyTemplate[]>([])
  const [selectedStrategy, setSelectedStrategy] = useState<string>('balanced')
  const [result, setResult] = useState<ScreeningResult | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [selectedStock, setSelectedStock] = useState<StockScore | null>(null)
  const [marketFilter, setMarketFilter] = useState<string>('all')
  const [error, setError] = useState<string | null>(null)

  const [searchQuery, setSearchQuery] = useState('')
  const [minScore, setMinScore] = useState(0)
  const [currentPage, setCurrentPage] = useState(1)
  const pageSize = 10

  // 市场数据质量状态（数据底座健康度）
  const [marketDataStatus, setMarketDataStatus] = useState<MarketDataStatus | null>(null)
  const [dataStatusError, setDataStatusError] = useState<string | null>(null)

  // 筛选 + 分页
  const filteredResults = useMemo(() => {
    if (!result) return []
    let list = result.results
    if (searchQuery.trim()) {
      const q = searchQuery.trim().toLowerCase()
      list = list.filter(
        (s) =>
          s.name.toLowerCase().includes(q) ||
          s.code.toLowerCase().includes(q)
      )
    }
    if (minScore > 0) {
      list = list.filter((s) => s.totalScore >= minScore)
    }
    return list
  }, [result, searchQuery, minScore])

  const totalPages = Math.max(1, Math.ceil(filteredResults.length / pageSize))
  const safePage = Math.min(currentPage, totalPages)
  const pagedResults = filteredResults.slice((safePage - 1) * pageSize, safePage * pageSize)

  // 筛选变化时重置到第1页
  useEffect(() => {
    setCurrentPage(1)
  }, [searchQuery, minScore, selectedStrategy, marketFilter])

  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    loadInitialData()
    return () => {
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
    }
  }, [])

  const loadInitialData = async (retryCount: number = 0, notified: boolean = false) => {
    setIsLoading(true)
    try {
      // 加载市场数据质量状态
      try {
        const ds = await getMarketDataStatus()
        setMarketDataStatus(ds)
        setDataStatusError(null)
      } catch (dsErr) {
        setDataStatusError(dsErr instanceof Error ? dsErr.message : String(dsErr))
      }

      const strats = await getStrategyList()
      setStrategies(strats)

      const screeningResult = await screenStock('balanced')
      setResult(screeningResult)
      setError(null)
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      const isEngineStarting = msg.includes('not initialized') || msg.includes('未启动')

      if (isEngineStarting && retryCount < 5) {
        setTimeout(() => loadInitialData(retryCount + 1, notified), 1500)
        return
      }

      console.error('Failed to load initial data:', err)
      const friendly = toFriendlyError(err)
      setError(friendly.message)
      // 数据获取失败：弹窗警示 + 30 秒自动重试，直到成功为止（首次失败弹一次）
      if (!notified) {
        notified = true
        useToastStore.getState().error(
          '选股引擎数据获取失败，将在30秒后自动重试',
          friendly.message
        )
      }
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      retryTimerRef.current = setTimeout(() => loadInitialData(retryCount, notified), 30000)
    } finally {
      setIsLoading(false)
    }
  }

  const handleScreen = async () => {
    setIsLoading(true)
    setSelectedStock(null)
    setError(null)
    try {
      const screeningResult = await screenStock(selectedStrategy, marketFilter)
      setResult(screeningResult)
    } catch (err) {
      console.error('Screening failed:', err)
      const friendly = toFriendlyError(err)
      setError(friendly.message)
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <div className="bg-slate-50 dark:bg-slate-900">
      {/* 顶部标题区 */}
      <div className="px-6 py-4 border-b border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-indigo-500 to-purple-500 flex items-center justify-center">
              <Search className="w-5 h-5 text-white" />
            </div>
            <div>
              <h1 className="text-xl font-bold text-slate-800 dark:text-slate-100 flex items-center gap-2">
                选股引擎
                <span className="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium bg-indigo-100 text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-400 rounded">
                  <Sparkles className="w-3 h-3" />
                  Agent 股票池
                </span>
              </h1>
              <p className="text-sm text-slate-500 dark:text-slate-400">
                QuantBot Core Score · 经典多因子选股 · 智能体使用此股票池进行选股分析
              </p>
            </div>
          </div>
        </div>

      </div>

      {/* 错误提示横幅 */}
      {error && (
        <div className="mx-6 mt-4 p-4 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 rounded-xl flex items-start gap-3">
          <div className="w-5 h-5 rounded-full bg-red-500 flex items-center justify-center flex-shrink-0 mt-0.5">
            <X className="w-3 h-3 text-white" />
          </div>
          <div className="flex-1">
            <p className="text-sm font-medium text-red-700 dark:text-red-300">{error}</p>
            <p className="text-xs text-red-500 dark:text-red-400 mt-1">请检查后端服务是否正常运行，或点击刷新按钮重试</p>
          </div>
          <button
            onClick={() => setError(null)}
            className="text-red-500 hover:text-red-700 dark:text-red-400 dark:hover:text-red-300"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
      )}

      {/* 数据质量状态卡片 */}
      {marketDataStatus && (
        <div className={`mx-6 mt-4 p-4 rounded-xl border flex items-center justify-between gap-4 ${
          marketDataStatus.ok
            ? 'bg-green-50 dark:bg-green-900/10 border-green-200 dark:border-green-800'
            : 'bg-amber-50 dark:bg-amber-900/10 border-amber-200 dark:border-amber-800'
        }`}>
          <div className="flex items-center gap-3">
            <div className={`w-9 h-9 rounded-full flex items-center justify-center flex-shrink-0 ${
              marketDataStatus.ok
                ? 'bg-green-500'
                : 'bg-amber-500'
            }`}>
              {marketDataStatus.ok
                ? <TrendingUp className="w-4 h-4 text-white" />
                : <X className="w-4 h-4 text-white" />}
            </div>
            <div>
              <p className={`text-sm font-semibold ${
                marketDataStatus.ok
                  ? 'text-green-700 dark:text-green-300'
                  : 'text-amber-700 dark:text-amber-300'
              }`}>
                {marketDataStatus.ok ? '数据状态：正常' : '数据状态：已过期'}
              </p>
              <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
                {marketDataStatus.reason}
              </p>
            </div>
          </div>
          <div className="flex items-center gap-5 text-xs text-slate-600 dark:text-slate-300">
            <div className="text-center">
              <p className="text-slate-400 dark:text-slate-500">最新日期</p>
              <p className="font-medium mt-0.5">{marketDataStatus.latest_date || '—'}</p>
            </div>
            <div className="text-center">
              <p className="text-slate-400 dark:text-slate-500">股票数量</p>
              <p className="font-medium mt-0.5">{marketDataStatus.stock_count.toLocaleString()}</p>
            </div>
            <div className="text-center">
              <p className="text-slate-400 dark:text-slate-500">缺失率</p>
              <p className="font-medium mt-0.5">{(marketDataStatus.missing_rate * 100).toFixed(1)}%</p>
            </div>
            <div className="text-center">
              <p className="text-slate-400 dark:text-slate-500">数据源</p>
              <p className="font-medium mt-0.5">{marketDataStatus.data_source || '—'}</p>
            </div>
            <button
              onClick={() => loadInitialData()}
              className="p-2 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-700 text-slate-500 dark:text-slate-400 transition-colors"
              title="刷新数据状态"
            >
              <RefreshCw className="w-4 h-4" />
            </button>
          </div>
        </div>
      )}
      {dataStatusError && !marketDataStatus && (
        <div className="mx-6 mt-4 p-3 rounded-xl border border-amber-200 dark:border-amber-800 bg-amber-50 dark:bg-amber-900/10 text-xs text-amber-700 dark:text-amber-300">
          数据状态获取失败：{dataStatusError}
        </div>
      )}

      {/* 主内容区 - 快速选股 */}
      <SimpleScreener
        strategies={strategies}
        selectedStrategy={selectedStrategy}
        setSelectedStrategy={setSelectedStrategy}
        marketFilter={marketFilter}
        setMarketFilter={setMarketFilter}
        onScreen={handleScreen}
        isLoading={isLoading}
        result={result}
        selectedStock={selectedStock}
        setSelectedStock={setSelectedStock}
      />
    </div>
  )
}

// ==================== 快速选股组件 ====================

function SimpleScreener(props: {
  strategies: StrategyTemplate[]
  selectedStrategy: string
  setSelectedStrategy: (s: string) => void
  marketFilter: string
  setMarketFilter: (m: string) => void
  onScreen: () => void
  isLoading: boolean
  result: ScreeningResult | null
  selectedStock: StockScore | null
  setSelectedStock: (s: StockScore | null) => void
}) {
  const { strategies, selectedStrategy, setSelectedStrategy, marketFilter, setMarketFilter, onScreen, isLoading, result, selectedStock, setSelectedStock } = props

  const [step, setStep] = useState(1)

  return (
    <div className="p-6">
      <div className="max-w-3xl mx-auto">
        {/* 步骤指示器 */}
        <div className="flex items-center justify-center gap-2 mb-8">
          {[1, 2, 3].map((s) => (
            <div key={s} className="flex items-center gap-2">
              <div className={`w-8 h-8 rounded-full flex items-center justify-center text-sm font-medium transition-all ${
                step >= s
                  ? 'bg-indigo-500 text-white'
                  : 'bg-slate-200 dark:bg-slate-700 text-slate-500'
              }`}>
                {step > s ? '✓' : s}
              </div>
              {s < 3 && (
                <div className={`w-16 h-0.5 transition-all ${step > s ? 'bg-indigo-500' : 'bg-slate-200 dark:bg-slate-700'}`} />
              )}
            </div>
          ))}
        </div>

        {/* Step 1: 选择策略 */}
        {step === 1 && (
          <div>
            <h2 className="text-lg font-bold text-slate-800 dark:text-slate-100 mb-2">选择选股策略</h2>
            <p className="text-sm text-slate-500 dark:text-slate-400 mb-6">
              选择一个适合你的投资风格的策略模板
            </p>
            <div className="grid grid-cols-3 gap-4">
              {strategies.map((strategy) => {
                const Icon = STRATEGY_ICONS[strategy.icon] || Shield
                const isSelected = selectedStrategy === strategy.id
                const formula = buildFormulaString(strategy.weights)
                return (
                  <button
                    key={strategy.id}
                    onClick={() => setSelectedStrategy(strategy.id)}
                    className={`p-5 rounded-xl border-2 text-left transition-all ${
                      isSelected
                        ? 'border-indigo-500 bg-indigo-50 dark:bg-indigo-900/30'
                        : 'border-slate-200 dark:border-slate-700 hover:border-indigo-300 dark:hover:border-indigo-700'
                    }`}
                  >
                    <Icon className={`w-8 h-8 mb-3 ${isSelected ? 'text-indigo-500' : 'text-slate-400'}`} />
                    <span className={`block font-semibold mb-1 ${isSelected ? 'text-indigo-600 dark:text-indigo-400' : 'text-slate-700 dark:text-slate-300'}`}>
                      {strategy.name}
                    </span>
                    <p className="text-xs text-slate-500 dark:text-slate-400 mb-2">{strategy.description}</p>
                    <div className={`text-xs p-2 rounded-lg font-mono break-all ${
                      isSelected
                        ? 'bg-indigo-100 dark:bg-indigo-800/40 text-indigo-700 dark:text-indigo-300'
                        : 'bg-slate-100 dark:bg-slate-600/30 text-slate-600 dark:text-slate-400'
                    }`}>
                      {formula}
                    </div>
                  </button>
                )
              })}
            </div>
            <div className="flex justify-end mt-8">
              <button
                onClick={() => setStep(2)}
                className="btn-primary"
              >
                下一步 →
              </button>
            </div>
          </div>
        )}

        {/* Step 2: 选择市场并执行 */}
        {step === 2 && (
          <div>
            <h2 className="text-lg font-bold text-slate-800 dark:text-slate-100 mb-2">选择市场并执行</h2>
            <p className="text-sm text-slate-500 dark:text-slate-400 mb-6">
              选择市场范围并开始选股
            </p>
            <div className="grid grid-cols-3 gap-4 mb-6">
              {[
                { value: 'all', label: '全部市场', desc: '沪深两市' },
                { value: 'sh', label: '沪市', desc: '上海证券交易所' },
                { value: 'sz', label: '深市', desc: '深圳证券交易所' },
              ].map((opt) => (
                <button
                  key={opt.value}
                  onClick={() => setMarketFilter(opt.value)}
                  className={`p-4 rounded-xl border-2 text-left transition-all ${
                    marketFilter === opt.value
                      ? 'border-indigo-500 bg-indigo-50 dark:bg-indigo-900/30'
                      : 'border-slate-200 dark:border-slate-700 hover:border-indigo-300 dark:hover:border-indigo-700'
                  }`}
                >
                  <span className={`block font-semibold ${marketFilter === opt.value ? 'text-indigo-600' : 'text-slate-700'}`}>
                    {opt.label}
                  </span>
                  <p className="text-xs text-slate-500 mt-1">{opt.desc}</p>
                </button>
              ))}
            </div>

            <div className="bg-slate-50 dark:bg-slate-800 rounded-lg p-4 mb-6">
              <div className="flex items-center justify-between text-sm">
                <span className="text-slate-500">已选策略</span>
                <span className="font-medium text-slate-700 dark:text-slate-300">
                  {strategies.find((s) => s.id === selectedStrategy)?.name || '-'}
                </span>
              </div>
              <div className="flex items-center justify-between text-sm mt-2">
                <span className="text-slate-500">市场范围</span>
                <span className="font-medium text-slate-700 dark:text-slate-300">
                  {marketFilter === 'all' ? '全部市场' : marketFilter === 'sh' ? '沪市' : '深市'}
                </span>
              </div>
            </div>

            <div className="flex justify-between">
              <button onClick={() => setStep(1)} className="btn-secondary">← 上一步</button>
              <button
                onClick={async () => {
                  await onScreen()
                  setStep(3)
                }}
                disabled={isLoading}
                className="btn-primary flex items-center gap-2"
              >
                {isLoading ? (
                  <>
                    <RefreshCw className="w-4 h-4 animate-spin" />
                    选股中...
                  </>
                ) : (
                  <>
                    <Search className="w-4 h-4" />
                    执行选股
                  </>
                )}
              </button>
            </div>
          </div>
        )}

        {/* Step 3: 查看结果 */}
        {step === 3 && (
          <div>
            <h2 className="text-lg font-bold text-slate-800 dark:text-slate-100 mb-2">选股结果</h2>
            <p className="text-sm text-slate-500 dark:text-slate-400 mb-6">
              为您筛选出 {result?.results.length || 0} 只优质股票
            </p>

            {result && result.results.length > 0 ? (
              <div className="space-y-3">
                {result.results.map((stock) => (
                  <button
                    key={stock.code}
                    onClick={() => setSelectedStock(stock)}
                    className={`w-full p-4 rounded-xl border transition-all text-left ${
                      selectedStock?.code === stock.code
                        ? 'border-indigo-500 bg-indigo-50 dark:bg-indigo-900/20'
                        : 'border-slate-200 dark:border-slate-700 hover:border-indigo-300'
                    }`}
                  >
                    <div className="flex items-center gap-4">
                      <div className={`w-12 h-12 rounded-lg flex items-center justify-center text-lg font-bold ${
                        stock.totalScore >= 75 ? 'bg-green-100 text-green-700' :
                        stock.totalScore >= 60 ? 'bg-amber-100 text-amber-700' :
                        'bg-red-100 text-red-700'
                      }`}>
                        {stock.totalScore.toFixed(0)}
                      </div>
                      <div className="flex-1">
                        <div className="flex items-center gap-2">
                          <span className="font-semibold text-slate-800 dark:text-slate-100">{stock.name}</span>
                          <span className="text-xs text-slate-400">{stock.code}</span>
                        </div>
                        <div className="flex items-center gap-3 mt-1">
                          <span className="text-sm text-slate-600">¥{stock.price.toFixed(2)}</span>
                          <span className={`text-sm ${stock.changePct >= 0 ? 'text-red-500' : 'text-green-500'}`}>
                            {stock.changePct >= 0 ? '+' : ''}{stock.changePct.toFixed(2)}%
                          </span>
                        </div>
                      </div>
                      <ChevronRight className="w-5 h-5 text-slate-300" />
                    </div>
                  </button>
                ))}
              </div>
            ) : (
              <div className="text-center py-12">
                <Search className="w-12 h-12 text-slate-300 dark:text-slate-600 mx-auto mb-4" />
                <p className="text-slate-500">没有符合条件的股票</p>
                <button onClick={() => setStep(1)} className="btn-secondary mt-4">重新选股</button>
              </div>
            )}

            <div className="flex justify-between mt-8">
              <button onClick={() => { setStep(1); setSelectedStock(null); }} className="btn-secondary">重新选股</button>
            </div>
          </div>
        )}
      </div>

      {/* 选中股票详情弹窗 */}
      {selectedStock && (
        <div className="fixed inset-0 bg-black/50 flex items-center justify-center z-50 p-4" onClick={() => setSelectedStock(null)}>
          <div
            className="bg-white dark:bg-slate-800 rounded-2xl max-w-3xl w-full max-h-[85vh] overflow-y-auto"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="flex items-center justify-between p-5 border-b border-slate-200 dark:border-slate-700 sticky top-0 bg-white dark:bg-slate-800">
              <div className="flex items-center gap-3">
                <BarChart3 className="w-5 h-5 text-indigo-500" />
                <h2 className="text-lg font-semibold text-slate-800 dark:text-slate-100">股票评分详情</h2>
              </div>
              <button
                onClick={() => setSelectedStock(null)}
                className="p-1.5 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-700"
              >
                <X className="w-5 h-5 text-slate-500" />
              </button>
            </div>
            <div className="p-5">
              <FactorScoreCard stock={selectedStock} />
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// ==================== Factor Score Card ====================

function FactorScoreCard({ stock }: {
  stock: StockScore
}) {
  return (
    <div>
      {/* 头部 */}
      <div className="text-center mb-6">
        <div className="inline-flex items-center justify-center w-16 h-16 rounded-2xl bg-gradient-to-br from-indigo-100 to-purple-100 dark:from-indigo-900/30 dark:to-purple-900/30 mb-3">
          <BarChart3 className="w-8 h-8 text-indigo-600 dark:text-indigo-400" />
        </div>
        <h2 className="text-xl font-bold text-slate-800 dark:text-slate-100">{stock.name}</h2>
        <p className="text-sm text-slate-500 dark:text-slate-400">{stock.market}{stock.code}</p>
      </div>

      {/* 总分展示 */}
      <div className="bg-gradient-to-r from-indigo-500 to-purple-500 rounded-xl p-4 mb-6 text-white">
        <div className="text-center">
          <p className="text-sm opacity-80 mb-1">QuantBot Core Score</p>
          <p className="text-4xl font-bold">{stock.totalScore.toFixed(1)}<span className="text-lg opacity-80"> / 100</span></p>
        </div>
      </div>

      {/* 因子得分 */}
      <div className="space-y-3 mb-6">
        <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300 flex items-center gap-2">
          <Eye className="w-4 h-4" />
          因子评分
        </h3>
        {(stock.factorScores || []).map((fs) => (
          <FactorBar key={fs.factorId} factorScore={fs} />
        ))}
      </div>

      {/* 选股理由 */}
      {(stock.reasons || []).length > 0 && (
        <div className="mb-4">
          <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300 mb-2 flex items-center gap-2">
            <Sparkles className="w-4 h-4 text-green-500" />
            Why?
          </h3>
          <ul className="space-y-1.5">
            {(stock.reasons || []).map((reason, idx) => (
              <li key={idx} className="flex items-center gap-2 text-sm text-slate-600 dark:text-slate-400">
                <span className="text-green-500">✓</span> {reason}
              </li>
            ))}
          </ul>
        </div>
      )}

      {/* 收益率指标 */}
      <div className="mb-4">
        <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300 mb-3 flex items-center gap-2">
          <TrendingUp className="w-4 h-4 text-indigo-500" />
          历史收益率
        </h3>
        <div className="grid grid-cols-4 gap-3">
          {[
            { label: '1月', value: stock.momentum1m },
            { label: '3月', value: stock.momentum3m },
            { label: '6月', value: stock.momentum6m },
            { label: '12月', value: stock.momentum12m },
          ].map((item) => (
            <div key={item.label} className="text-center p-2 rounded-lg bg-slate-50 dark:bg-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400">{item.label}</div>
              <div className={`text-sm font-semibold ${
                (item.value || 0) >= 0 ? 'text-red-500' : 'text-green-500'
              }`}>
                {item.value !== undefined && item.value !== null && item.value !== 0
                  ? `${item.value >= 0 ? '+' : ''}${item.value.toFixed(2)}%`
                  : '--'}
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* 风险指标 */}
      <div className="mb-4">
        <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300 mb-3 flex items-center gap-2">
          <Shield className="w-4 h-4 text-purple-500" />
          风险指标
        </h3>
        <div className="grid grid-cols-4 gap-3">
          {[
            { label: '波动率', value: stock.volatility, unit: '%' },
            { label: '振幅', value: stock.amplitude, unit: '%' },
            { label: '换手率', value: stock.turnoverRate, unit: '%' },
            { label: '流动性', value: stock.liquidity, unit: '' },
          ].map((item) => (
            <div key={item.label} className="text-center p-2 rounded-lg bg-slate-50 dark:bg-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400">{item.label}</div>
              <div className="text-sm font-semibold text-slate-700 dark:text-slate-300">
                {item.value !== undefined && item.value !== null && item.value !== 0
                  ? `${item.value.toFixed(2)}${item.unit}`
                  : '--'}
              </div>
            </div>
          ))}
        </div>
      </div>

      {/* 风险提示 */}
      {(stock.warnings || []).length > 0 && (
        <div className="mb-4">
          <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300 mb-2 flex items-center gap-2">
            <Shield className="w-4 h-4 text-amber-500" />
            风险提示
          </h3>
          <ul className="space-y-1.5">
            {(stock.warnings || []).map((warning, idx) => (
              <li key={idx} className="flex items-center gap-2 text-sm text-slate-600 dark:text-slate-400">
                <span className="text-amber-500">⚠</span> {warning}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

// ==================== 因子进度条 ====================

function FactorBar({ factorScore }: { factorScore: FactorScore }) {
  const colors = FACTOR_COLORS[factorScore.factorId] || FACTOR_COLORS.value
  const width = Math.min(100, factorScore.score)

  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <span className="text-sm text-slate-600 dark:text-slate-300">{factorScore.factorName}</span>
        <span className={`text-sm font-semibold ${colors.text}`}>{factorScore.score.toFixed(1)}</span>
      </div>
      <div className="h-2 bg-slate-200 dark:bg-slate-600 rounded-full overflow-hidden">
        <div
          className={`h-full ${colors.bar} rounded-full transition-all duration-500`}
          style={{ width: `${width}%` }}
        />
      </div>
    </div>
  )
}
