import { useState, useEffect, useCallback, useRef } from 'react'
import {
  Shield,
  Cpu,
  Activity,
  Pause,
  Square,
  RefreshCw,
  FileText,
  TrendingUp,
  Target,
  Calendar,
  BarChart3,
  Lightbulb,
} from 'lucide-react'
import {
  GetDailyReviews,
  GenerateDailyReview,
  RunCIODailyCheck,
  GetCIOStatus,
  GetCIOJournal,
  GetTodayDecisions,
  GetCurrentMarketState,
  GetPortfolioState,
  GetPortfolioRiskMetrics,
  ResumeTrading,
  EmergencyStop,
} from '../../wailsjs/go/main/App'
import { formatCurrency } from '../utils/formatters'
import { getCurrentPhase } from '../utils/marketPhase'
import { useToastStore } from '../store/toastStore'

interface JournalEntry {
  timestamp: string
  decision: string
  reason: string
  riskApproval: string
  policyStatus: string
}

interface TodayDecision {
  decision: string
  reason: string
  riskApproval: string
  policyStatus: string
  marketState: string
  marketConfidence: number
  timestamp: string
}

interface DailyReview {
  id: number
  review_date: string
  total_assets: number
  total_return: number
  daily_pnl: number
  daily_return: number
  cash: number
  market_value: number
  positions_count: number
  trade_count: number
  trade_pnl: number
  market_regime: string
  market_confidence: number
  top_gainers: string
  top_losers: string
  risk_metrics: string
  key_events: string
  decisions: string
  position_strategy_review: string
  strategy_change_suggestion: string
  agent_summaries: string
  summary: string
  recommendations: string
  status: string
  created_at: string
}

const formatDecisionTime = (timestamp: string): string => {
  if (!timestamp) return '--'
  try {
    const date = new Date(timestamp)
    if (!isNaN(date.getTime())) {
      return date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
    }
    const timeMatch = timestamp.match(/(\d{1,2}):(\d{2})(?::(\d{2}))?/)
    if (timeMatch) {
      const h = timeMatch[1].padStart(2, '0')
      const m = timeMatch[2]
      return `${h}:${m}`
    }
    return timestamp
  } catch {
    return timestamp
  }
}

// 安全解析JSON字符串（数组），失败返回空数组
const safeParseArray = (s?: string): any[] => {
  if (!s) return []
  try {
    const parsed = JSON.parse(s)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

// 安全解析JSON字符串（对象），失败返回空对象
const safeParseObject = (s?: string): Record<string, any> => {
  if (!s) return {}
  try {
    const parsed = JSON.parse(s)
    return parsed && typeof parsed === 'object' ? parsed : {}
  } catch {
    return {}
  }
}

const decisionTypeLabel: Record<string, string> = {
  BUY: '买入',
  BUILD: '建仓',
  SELL: '卖出',
  HOLD: '持有',
  REDUCE: '减仓',
  REDUCE_RISK: '减仓降风险',
  INCREASE: '加仓',
  INCREASE_RISK: '增仓进取',
  REBALANCE: '调仓',
  PAUSE: '暂停',
  PAUSE_TRADING: '暂停交易',
  NO_ACTION: '观望',
  REQUEST_RESEARCH: '请求研究',
  REQUEST_RISK_REVIEW: '请求风控审查',
  CHANGE_STRATEGY: '策略变更',
}

// 市场状态中文映射
const marketStateLabel: Record<string, string> = {
  BULLISH: '牛市',
  BEARISH: '熊市',
  NEUTRAL: '震荡',
  UP: '上涨趋势',
  DOWN: '下跌趋势',
  STABLE: '稳定',
  HIGH_VOLATILITY: '高波动',
  LOW_VOLATILITY: '低波动',
  CRASH: '暴跌',
  RALLY: '反弹',
}

const decisionTypeColor: Record<string, string> = {
  BUY: 'bg-green-500',
  BUILD: 'bg-green-500',
  SELL: 'bg-red-500',
  REDUCE: 'bg-orange-500',
  REDUCE_RISK: 'bg-orange-500',
  INCREASE: 'bg-blue-500',
  INCREASE_RISK: 'bg-blue-500',
  REBALANCE: 'bg-purple-500',
  PAUSE: 'bg-yellow-500',
  PAUSE_TRADING: 'bg-yellow-500',
  NO_ACTION: 'bg-slate-500',
  HOLD: 'bg-slate-500',
}

export default function CIO() {
  const showError = useToastStore((s) => s.error)
  const showWarning = useToastStore((s) => s.warning)
  const [cioStatusState, setCioStatusState] = useState<any>(null)
  const [journal, setJournal] = useState<JournalEntry[]>([])
  const [todayDecisions, setTodayDecisions] = useState<TodayDecision[]>([])
  const [reviews, setReviews] = useState<DailyReview[]>([])
  const [loadingReviews, setLoadingReviews] = useState(false)
  const [generatingReview, setGeneratingReview] = useState(false)
  const [activeTab, setActiveTab] = useState<'decision' | 'review'>('decision')
  const [loading, setLoading] = useState(true)
  const [currentMarketState, setCurrentMarketState] = useState<any>(null)
  const [riskMetrics, setRiskMetrics] = useState<any>(null)

  // CIO 日志分页状态
  const [journalPage, setJournalPage] = useState(1)
  const journalPageSize = 5

  // 数据获取失败自动重试：标记去重
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const reviewNotifiedRef = useRef(false)

  const loadAll = useCallback(async (): Promise<boolean> => {
    setLoading(true)
    let anyFailed = false
    try {
      const results = await Promise.allSettled([
        GetCIOStatus(),
        GetCIOJournal(7),
        GetTodayDecisions(),
        GetPortfolioState(),
        GetCurrentMarketState(),
        GetPortfolioRiskMetrics(),
      ])

      const [statusResult, journalResult, decisionsResult, portfolioResult, marketStateResult, riskResult] = results

      if (statusResult.status === 'fulfilled' && statusResult.value) {
        setCioStatusState(statusResult.value)
      } else {
        anyFailed = true
        showError('CIO状态加载失败', '无法获取CIO运行状态')
      }

      if (journalResult.status === 'fulfilled' && Array.isArray(journalResult.value?.entries)) {
        setJournal(journalResult.value.entries as JournalEntry[])
      } else {
        anyFailed = true
        showWarning('日志加载异常', '无法获取CIO决策日志')
      }

      if (decisionsResult.status === 'fulfilled' && decisionsResult.value?.decisions) {
        setTodayDecisions(decisionsResult.value.decisions as TodayDecision[])
      } else {
        anyFailed = true
        showWarning('决策加载异常', '无法获取今日决策数据')
      }

      if (portfolioResult.status === 'fulfilled' && portfolioResult.value) {
        // Portfolio state loaded but not directly displayed on this page
      } else {
        anyFailed = true
        showWarning('组合数据加载异常', '无法获取投资组合状态')
      }

      if (marketStateResult.status === 'fulfilled' && marketStateResult.value) {
        setCurrentMarketState(marketStateResult.value)
      } else {
        anyFailed = true
      }

      if (riskResult.status === 'fulfilled' && riskResult.value) {
        setRiskMetrics(riskResult.value)
      } else {
        anyFailed = true
      }
    } catch (err) {
      anyFailed = true
      const errMsg = err instanceof Error ? err.message : String(err)
      showError('CIO数据加载失败', `加载过程出现异常: ${errMsg}`)
    } finally {
      setLoading(false)
    }
    return anyFailed
  }, [showError, showWarning])

  const loadReviews = async () => {
    setLoadingReviews(true)
    try {
      const result = await GetDailyReviews(5)
      if (result && result.reviews) {
        setReviews(result.reviews)
      }
      reviewNotifiedRef.current = false
    } catch (err) {
      console.error('Failed to load daily reviews:', err)
      // 数据获取失败：弹窗警示 + 30 秒自动重试（首次失败弹一次）
      if (!reviewNotifiedRef.current) {
        reviewNotifiedRef.current = true
        showError('复盘记录加载失败', err instanceof Error ? err.message : String(err))
      }
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      retryTimerRef.current = setTimeout(async () => {
        await loadAll()
        await loadReviews()
      }, 30000)
    } finally {
      setLoadingReviews(false)
    }
  }

  const handleGenerateReview = async () => {
    setGeneratingReview(true)
    try {
      const result = await GenerateDailyReview()
      if (result && result.success) {
        await loadReviews()
      }
    } catch (err) {
      console.error('Failed to generate daily review:', err)
    } finally {
      setGeneratingReview(false)
    }
  }

  const handleRunDailyCheck = async () => {
    try {
      await RunCIODailyCheck()
      await loadAll()
    } catch (err) {
      console.error('Failed to run daily check:', err)
    }
  }

  const handleResume = async () => {
    try {
      await ResumeTrading()
      await loadAll()
    } catch (err) {
      console.error('Resume failed:', err)
    }
  }

  const handleEmergencyStop = async () => {
    try {
      await EmergencyStop()
      await loadAll()
    } catch (err) {
      console.error('Emergency stop failed:', err)
    }
  }

  useEffect(() => {
    const init = async () => {
      const failed = await loadAll()
      if (failed) {
        // 数据获取失败：首次弹窗已在 loadAll 内弹出，这里安排 30 秒自动重试
        if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
        retryTimerRef.current = setTimeout(async () => {
          await loadAll()
          await loadReviews()
        }, 30000)
      }
    }
    init()
    loadReviews()
    return () => {
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      reviewNotifiedRef.current = false
    }
  }, [loadAll])

  // 如果没有决策记录，使用当前市场状态创建一个虚拟决策
  // 盘前(9:15前)或休市时不应显示"已完成决策"的时间戳，避免误导
  // 无真实决策时一律不显示时间戳（显示"--"），避免把当前时间误当决策完成时间
  const currentPhase = getCurrentPhase()
  const decisionPending = currentPhase === 'PRE_MARKET' || currentPhase === 'CLOSED'
  const todayDecision = todayDecisions.length > 0 
    ? todayDecisions[0] 
    : (currentMarketState ? {
        decision: 'NO_ACTION',
        reason: decisionPending
          ? (currentPhase === 'CLOSED' ? '当前休市，等待下一交易日' : '盘前准备中，等待开盘后（9:15）生成决策')
          : '等待决策生成，保持观望',
        riskApproval: 'APPROVED',
        policyStatus: 'PASSED',
        marketState: currentMarketState.regime,
        marketConfidence: currentMarketState.confidence,
        timestamp: '',
      } : null)

  const isRunning = cioStatusState?.state === 'RUNNING' || cioStatusState?.state === 'ACTIVE'
  const isPaused = cioStatusState?.state === 'PAUSED' || cioStatusState?.emergency

  const statusBadge = () => {
    if (cioStatusState?.emergency) {
      return (
        <span className="badge bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400">
          <Square className="w-3 h-3 mr-1" />
          紧急停止
        </span>
      )
    }
    if (isPaused) {
      return (
        <span className="badge bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400">
          <Pause className="w-3 h-3 mr-1" />
          已暂停
        </span>
      )
    }
    if (isRunning) {
      return (
        <span className="badge bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400">
          <span className="w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse mr-1" />
          运行中
        </span>
      )
    }
    return (
      <span className="badge bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-400">
        <span className="w-1.5 h-1.5 rounded-full bg-slate-400 mr-1" />
        就绪
      </span>
    )
  }

  return (
    <div className="space-y-5">
      {/* Header */}
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-lg bg-gradient-to-br from-brand-500 to-cyan-400 flex items-center justify-center shadow-sm">
            <Shield className="w-5 h-5 text-white" />
          </div>
          <div>
            <h1 className="text-lg font-bold text-slate-800 dark:text-slate-100">投资管理</h1>
            <p className="text-xs text-slate-500 dark:text-slate-400">投资决策中心 · 系统级投资管理</p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          {statusBadge()}
          <span className="text-sm text-slate-500 dark:text-slate-400">{new Date().toISOString().split('T')[0]}</span>
        </div>
      </header>

      {/* Tab Switcher */}
      <div className="flex items-center gap-2 p-1 bg-slate-100 dark:bg-slate-800 rounded-lg">
        <button
          onClick={() => setActiveTab('decision')}
          className={`flex-1 py-2 px-4 rounded-md text-sm font-medium transition-all ${
            activeTab === 'decision'
              ? 'bg-white dark:bg-slate-700 text-brand-600 dark:text-brand-400 shadow-sm'
              : 'text-slate-500 dark:text-slate-400 hover:text-slate-700 dark:hover:text-slate-300'
          }`}
        >
          <Target className="w-4 h-4 inline-block mr-1" />
          投资决策
        </button>
        <button
          onClick={() => setActiveTab('review')}
          className={`flex-1 py-2 px-4 rounded-md text-sm font-medium transition-all ${
            activeTab === 'review'
              ? 'bg-white dark:bg-slate-700 text-brand-600 dark:text-brand-400 shadow-sm'
              : 'text-slate-500 dark:text-slate-400 hover:text-slate-700 dark:hover:text-slate-300'
          }`}
        >
          <Calendar className="w-4 h-4 inline-block mr-1" />
          每日复盘
        </button>
      </div>

      {/* Decision Tab */}
      {activeTab === 'decision' && (
        <>
      {/* Today's Decision */}
      <div className="card p-5">
        <div className="flex items-center gap-2 mb-4">
          <Target className="w-4 h-4 text-brand-500" />
          <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">今日决策</h2>
        </div>

        {loading ? (
          <div className="flex items-center justify-center py-12">
            <RefreshCw className="w-5 h-5 animate-spin text-slate-400" />
            <span className="ml-2 text-sm text-slate-500">加载决策中...</span>
          </div>
        ) : todayDecision ? (
          <div className="flex items-center gap-4">
            <div className={`min-w-[80px] h-14 rounded-xl flex items-center justify-center text-white font-bold text-sm px-4 ${decisionTypeColor[todayDecision.decision] || 'bg-slate-500'}`}>
              {decisionTypeLabel[todayDecision.decision] || todayDecision.decision}
            </div>

            <div className="flex-1">
              <div className="flex items-center gap-2 mb-1">
                <span className="text-xs text-slate-500 dark:text-slate-400">决策类型</span>
                <span className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                  {decisionTypeLabel[todayDecision.decision] || todayDecision.decision}
                </span>
              </div>
              <div className="flex items-center gap-4 text-sm text-slate-600 dark:text-slate-400">
                {todayDecision.marketState && (
                  <span>
                    <span className="text-slate-400 dark:text-slate-500 mr-1">市场</span>
                    {marketStateLabel[todayDecision.marketState] || todayDecision.marketState}
                  </span>
                )}
                {todayDecision.marketConfidence > 0 && (
                  <span>
                    <span className="text-slate-400 dark:text-slate-500 mr-1">信心</span>
                    {(todayDecision.marketConfidence * 100).toFixed(0)}%
                  </span>
                )}
                {todayDecision.riskApproval && (
                  <span>
                    <span className="text-slate-400 dark:text-slate-500 mr-1">风控</span>
                    {todayDecision.riskApproval === 'APPROVED' ? '✓ 通过' : todayDecision.riskApproval === 'REJECTED' ? '✗ 否决' : '审核中'}
                  </span>
                )}
              </div>
            </div>

            <div className="text-right">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">决策时间</div>
              <div className="text-sm font-bold text-brand-600 dark:text-brand-400">
                {formatDecisionTime(todayDecision.timestamp)}
              </div>
            </div>
          </div>
        ) : (
          <div className="flex flex-col items-center justify-center py-10 text-center">
            <Target className="w-10 h-10 text-slate-300 dark:text-slate-600 mb-3" />
            <p className="text-sm text-slate-500 dark:text-slate-400">今日暂无决策记录</p>
            <p className="text-xs text-slate-400 mt-1">点击「运行每日检查」生成决策</p>
          </div>
        )}

        {todayDecision && (
          <div className="mt-4 pt-4 border-t border-slate-100 dark:border-slate-700">
            <p className="text-sm text-slate-600 dark:text-slate-400 leading-relaxed">
              <span className="text-slate-400 dark:text-slate-500 mr-2">决策说明：</span>
              {todayDecision.reason}
            </p>
          </div>
        )}
      </div>

      {/* 风险评价（VaR / ES）及含义 */}
      <div className="card p-5">
        <div className="flex items-center gap-2 mb-4">
          <BarChart3 className="w-4 h-4 text-brand-500" />
          <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">风险评价 · VaR / ES</h2>
          <span className="text-xs text-slate-400 dark:text-slate-500 ml-auto">历史法 · 基于真实组合结算收益</span>
        </div>

        {riskMetrics ? (
          <>
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
              <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">VaR(95%)</div>
                <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                  {typeof riskMetrics.var95_daily_return_pct === 'number'
                    ? `${riskMetrics.var95_daily_return_pct.toFixed(2)}%`
                    : 'N/A'}
                </div>
              </div>
              <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">VaR(99%)</div>
                <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                  {typeof riskMetrics.var99_daily_return_pct === 'number'
                    ? `${riskMetrics.var99_daily_return_pct.toFixed(2)}%`
                    : 'N/A'}
                </div>
              </div>
              <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">ES(95%) 期望短缺</div>
                <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                  {typeof riskMetrics.cvar95_daily_return_pct === 'number'
                    ? `${riskMetrics.cvar95_daily_return_pct.toFixed(2)}%`
                    : 'N/A'}
                </div>
              </div>
              <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">年化波动 / 最大回撤</div>
                <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                  {typeof riskMetrics.annualized_volatility_pct === 'number'
                    ? `${riskMetrics.annualized_volatility_pct.toFixed(1)}%`
                    : 'N/A'}
                  {' / '}
                  {typeof riskMetrics.max_drawdown_pct === 'number'
                    ? `${riskMetrics.max_drawdown_pct.toFixed(2)}%`
                    : 'N/A'}
                </div>
              </div>
            </div>
            <div className="mt-3 p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
              <p className="text-xs text-slate-600 dark:text-slate-400 leading-relaxed">
                <strong className="text-slate-700 dark:text-slate-300">含义说明：</strong>
                <span className="font-semibold text-slate-700 dark:text-slate-300">VaR（Value at Risk，风险价值）</span>表示在给定置信度下，未来一个交易日组合可能遭受的最大损失。例如 VaR(95%) = -1.50%，意为有 95% 的把握单日亏损不超过 1.50%。
                <span className="font-semibold text-slate-700 dark:text-slate-300">ES（Expected Shortfall，期望短缺 / CVaR）</span>则衡量所有超过 VaR 阈值的最坏情形下的平均损失，反映尾部风险。VaR 只回答"最坏损失的临界点"，ES 回答"真正出险时平均会亏多少"，前者管大概率、后者管极端尾部。<span className="text-slate-500 dark:text-slate-400">（数值为负表示日收益损失，数据源为真实组合结算收益）</span>
              </p>
            </div>
          </>
        ) : (
          <div className="flex flex-col items-center justify-center py-6 text-center">
            <BarChart3 className="w-8 h-8 text-slate-300 dark:text-slate-600 mb-2" />
            <p className="text-xs text-slate-500 dark:text-slate-400">
              {typeof riskMetrics?.note === 'string' ? riskMetrics.note : '暂无风险评价数据'}
            </p>
          </div>
        )}
      </div>

      {/* CIO Journal */}
      <div className="card p-5">
        <div className="flex items-center justify-between mb-4">
          <div className="flex items-center gap-2">
            <FileText className="w-4 h-4 text-brand-500" />
            <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">CIO 日志 · 投资决策记录</h2>
            {journal.length > 0 && (
              <span className="text-xs text-slate-400">（共 {journal.length} 条）</span>
            )}
          </div>
          {journal.length > journalPageSize && (
            <div className="flex items-center gap-1">
              <button
                onClick={() => setJournalPage(p => Math.max(1, p - 1))}
                disabled={journalPage <= 1}
                className="px-2 py-1 text-xs rounded border border-slate-200 dark:border-slate-600 disabled:opacity-50 hover:bg-slate-100 dark:hover:bg-slate-700"
              >
                上一页
              </button>
              <span className="text-xs text-slate-500 px-2">
                {journalPage} / {Math.ceil(journal.length / journalPageSize)}
              </span>
              <button
                onClick={() => setJournalPage(p => Math.min(Math.ceil(journal.length / journalPageSize), p + 1))}
                disabled={journalPage >= Math.ceil(journal.length / journalPageSize)}
                className="px-2 py-1 text-xs rounded border border-slate-200 dark:border-slate-600 disabled:opacity-50 hover:bg-slate-100 dark:hover:bg-slate-700"
              >
                下一页
              </button>
            </div>
          )}
        </div>

        {loading ? (
          <div className="flex items-center justify-center py-6">
            <RefreshCw className="w-4 h-4 animate-spin text-slate-400" />
          </div>
        ) : journal.length > 0 ? (
          <div className="space-y-2">
            {journal.slice((journalPage - 1) * journalPageSize, journalPage * journalPageSize).map((entry, i) => (
              <div
                key={i}
                className="flex items-start gap-3 p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50"
              >
                <span className="text-sm font-mono text-slate-400 dark:text-slate-500 w-16 shrink-0">
                  {entry.timestamp ? new Date(entry.timestamp).toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : '--'}
                </span>
                <span className="text-sm">
                  {decisionTypeColor[entry.decision] ? '📋' : '📝'}
                </span>
                <div className="flex-1">
                  <div className="flex items-center gap-2 mb-0.5">
                    <span className="text-xs font-medium text-slate-700 dark:text-slate-300">
                      {decisionTypeLabel[entry.decision] || entry.decision}
                    </span>
                    {entry.riskApproval && (
                      <span className={`text-[10px] px-1.5 py-0.5 rounded ${
                        entry.riskApproval === 'APPROVED' ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' :
                        entry.riskApproval === 'REJECTED' ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400' :
                        'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
                      }`}>
                        {entry.riskApproval === 'APPROVED' ? '✓ 通过' : entry.riskApproval === 'REJECTED' ? '✗ 否决' : '审核中'}
                      </span>
                    )}
                  </div>
                  <p className="text-xs text-slate-600 dark:text-slate-400 leading-relaxed">{entry.reason}</p>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <p className="text-sm text-slate-500 dark:text-slate-400 text-center py-6">暂无决策日志</p>
        )}
      </div>
        </>
      )}

      {/* Review Tab */}
      {activeTab === 'review' && (
        <>
          {/* Review List */}
          {loadingReviews ? (
            <div className="card p-8 flex items-center justify-center">
              <RefreshCw className="w-6 h-6 animate-spin text-slate-400" />
              <span className="ml-2 text-slate-500">加载复盘报告...</span>
            </div>
          ) : reviews.length === 0 ? (
            <div className="card p-8 text-center">
              <Calendar className="w-12 h-12 mx-auto text-slate-300 dark:text-slate-600 mb-4" />
              <h3 className="text-lg font-medium text-slate-700 dark:text-slate-300 mb-2">暂无复盘报告</h3>
              <p className="text-sm text-slate-500 dark:text-slate-400 mb-4">
                今日还没有生成复盘报告。点击上方按钮手动生成，或等待系统在收盘后自动生成。
              </p>
              <button
                onClick={handleGenerateReview}
                disabled={generatingReview}
                className="btn-primary disabled:opacity-50"
              >
                生成今日复盘
              </button>
            </div>
          ) : (
            <div className="space-y-4">
              {reviews.map((review) => (
                <div key={review.id} className="card p-5">
                  <div className="flex items-center justify-between mb-4">
                    <div className="flex items-center gap-3">
                      <div className="w-10 h-10 rounded-lg bg-brand-100 dark:bg-brand-900/30 flex items-center justify-center">
                        <Calendar className="w-5 h-5 text-brand-500" />
                      </div>
                      <div>
                        <h3 className="text-base font-semibold text-slate-800 dark:text-slate-100">
                          {review.review_date} 复盘报告
                        </h3>
                        <p className="text-xs text-slate-500 dark:text-slate-400">
                          生成于 {new Date(review.created_at).toLocaleString('zh-CN')}
                        </p>
                      </div>
                    </div>
                    <span className={`badge ${review.status === 'COMPLETED' ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' : 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'}`}>
                      {review.status === 'COMPLETED' ? '已完成' : '进行中'}
                    </span>
                  </div>

                  {/* Key Metrics */}
                  <div className="grid grid-cols-4 gap-4 mb-4">
                    <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                      <div className="text-xs text-slate-500 dark:text-slate-400">总资产</div>
                      <div className="text-lg font-semibold text-slate-800 dark:text-slate-100">
                        {formatCurrency(review.total_assets)}
                      </div>
                    </div>
                    <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                      <div className="text-xs text-slate-500 dark:text-slate-400">今日盈亏</div>
                      <div className={`text-lg font-semibold ${review.daily_pnl >= 0 ? 'text-green-600 dark:text-green-400' : 'text-red-600 dark:text-red-400'}`}>
                        {review.daily_pnl >= 0 ? '+' : ''}{formatCurrency(review.daily_pnl)}
                      </div>
                    </div>
                    <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                      <div className="text-xs text-slate-500 dark:text-slate-400">今日收益率</div>
                      <div className={`text-lg font-semibold ${review.daily_return >= 0 ? 'text-green-600 dark:text-green-400' : 'text-red-600 dark:text-red-400'}`}>
                        {review.daily_return >= 0 ? '+' : ''}{review.daily_return?.toFixed(2)}%
                      </div>
                    </div>
                    <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                      <div className="text-xs text-slate-500 dark:text-slate-400">持仓数量</div>
                      <div className="text-lg font-semibold text-slate-800 dark:text-slate-100">
                        {review.positions_count}
                      </div>
                    </div>
                  </div>

                  {/* Market Status */}
                  <div className="flex items-center gap-4 mb-4 p-3 rounded-lg bg-blue-50 dark:bg-blue-900/20 border border-blue-100 dark:border-blue-800">
                    <Activity className="w-5 h-5 text-blue-500" />
                    <div className="flex-1">
                      <span className="text-xs text-slate-500 dark:text-slate-400">市场状态：</span>
                      <span className="text-sm font-medium text-slate-700 dark:text-slate-300">
                        {review.market_regime === 'BULLISH' ? '牛市' :
                         review.market_regime === 'BEARISH' ? '熊市' :
                         review.market_regime === 'NEUTRAL' ? '震荡市' : review.market_regime}
                      </span>
                    </div>
                    <div className="text-right">
                      <span className="text-xs text-slate-500 dark:text-slate-400">信心指数：</span>
                      <span className="text-sm font-medium text-slate-700 dark:text-slate-300">
                        {(review.market_confidence * 100)?.toFixed(1)}%
                      </span>
                    </div>
                  </div>

                  {/* Summary */}
                  <div className="mb-4">
                    <div className="flex items-center gap-2 mb-2">
                      <FileText className="w-4 h-4 text-brand-500" />
                      <span className="text-sm font-medium text-slate-700 dark:text-slate-300">复盘摘要</span>
                    </div>
                    <p className="text-sm text-slate-600 dark:text-slate-400 leading-relaxed">
                      {review.summary}
                    </p>
                  </div>

                  {/* 各智能体复盘汇总 */}
                  {(() => {
                    const agentSummaries = safeParseObject(review.agent_summaries)
                    const roleNames: Record<string, string> = {
                      PLANNER: '规划师 (Planner)',
                      QUANT: '量化分析师 (Quant)',
                      RISK: '风控 (Risk)',
                      TRADER: '交易员 (Trader)',
                      CIO: '首席投资官 (CIO)',
                    }
                    const roles = ['PLANNER', 'QUANT', 'RISK', 'TRADER', 'CIO']
                    const hasData = roles.some(r => Array.isArray(agentSummaries[r]) && agentSummaries[r].length > 0)
                    if (!hasData) return null
                    return (
                      <div className="mb-4">
                        <div className="flex items-center gap-2 mb-2">
                          <Shield className="w-4 h-4 text-indigo-500" />
                          <span className="text-sm font-medium text-slate-700 dark:text-slate-300">各智能体复盘汇总</span>
                        </div>
                        <div className="space-y-2">
                          {roles.map((role) => {
                            const items = agentSummaries[role]
                            if (!Array.isArray(items) || items.length === 0) return null
                            return (
                              <div key={role} className="rounded-lg border border-slate-200 dark:border-slate-700 p-3">
                                <div className="flex items-center gap-1.5 mb-1.5">
                                  <span className="inline-flex items-center px-2 py-0.5 rounded bg-indigo-50 dark:bg-indigo-900/30 text-indigo-600 dark:text-indigo-400 text-xs font-medium">
                                    {roleNames[role] || role}
                                  </span>
                                </div>
                                {items.map((item: any, idx: number) => (
                                  <div key={idx} className="mb-1.5 last:mb-0">
                                    <div className="text-xs text-slate-400 dark:text-slate-500">
                                      {item.task_name}
                                      {item.deliverable_type ? ` · ${item.deliverable_type}` : ''}
                                      {item.task_phase ? ` · ${item.task_phase}` : ''}
                                    </div>
                                    {item.summary ? (
                                      <p className="text-xs text-slate-600 dark:text-slate-400 leading-relaxed mt-0.5">{item.summary}</p>
                                    ) : null}
                                  </div>
                                ))}
                              </div>
                            )
                          })}
                        </div>
                      </div>
                    )
                  })()}

                  {/* 持仓策略解读（Quant 复盘） */}
                  {(() => {
                    const strategies = safeParseArray(review.position_strategy_review)
                    if (strategies.length === 0) return null
                    return (
                      <div className="mb-4">
                        <div className="flex items-center gap-2 mb-2">
                          <TrendingUp className="w-4 h-4 text-cyan-500" />
                          <span className="text-sm font-medium text-slate-700 dark:text-slate-300">持仓策略解读（Quant 复盘）</span>
                        </div>
                        <div className="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-700">
                          <table className="w-full text-xs">
                            <thead className="bg-slate-50 dark:bg-slate-900/50 text-slate-500 dark:text-slate-400">
                              <tr>
                                <th className="px-3 py-2 text-left font-medium">股票</th>
                                <th className="px-3 py-2 text-left font-medium">执行策略</th>
                                <th className="px-3 py-2 text-left font-medium">策略状态</th>
                                <th className="px-3 py-2 text-right font-medium">收益率</th>
                                <th className="px-3 py-2 text-right font-medium">权重</th>
                                <th className="px-3 py-2 text-left font-medium">表现解读</th>
                              </tr>
                            </thead>
                            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                              {strategies.map((s: any, i: number) => (
                                <tr key={i}>
                                  <td className="px-3 py-2 font-medium text-slate-700 dark:text-slate-300">
                                    {s.name}
                                    <span className="text-slate-400 ml-1">{s.code}</span>
                                  </td>
                                  <td className="px-3 py-2 text-slate-600 dark:text-slate-400">{s.strategy}</td>
                                  <td className="px-3 py-2">
                                    <span className={`px-1.5 py-0.5 rounded text-[10px] ${
                                      s.status === '已触发止盈' ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' :
                                      s.status === '已触发止损' ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400' :
                                      s.status === '接近止损线' ? 'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400' :
                                      'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-400'
                                    }`}>
                                      {s.status}
                                    </span>
                                  </td>
                                  <td className={`px-3 py-2 text-right font-medium ${s.return >= 0 ? 'text-red-600 dark:text-red-400' : 'text-green-600 dark:text-green-400'}`}>
                                    {s.return >= 0 ? '+' : ''}{s.return?.toFixed(2)}%
                                  </td>
                                  <td className="px-3 py-2 text-right text-slate-600 dark:text-slate-400">{s.weight?.toFixed(1)}%</td>
                                  <td className="px-3 py-2 text-slate-600 dark:text-slate-400">{s.interpretation}</td>
                                </tr>
                              ))}
                            </tbody>
                          </table>
                        </div>
                      </div>
                    )
                  })()}

                  {/* 策略调整建议（Quant 复盘） */}
                  {(() => {
                    const suggestion = safeParseObject(review.strategy_change_suggestion)
                    if (Object.keys(suggestion).length === 0) return null
                    const needChange = suggestion.change_strategy === true
                    return (
                      <div className="mb-4">
                        <div className="flex items-center gap-2 mb-2">
                          <Cpu className="w-4 h-4 text-purple-500" />
                          <span className="text-sm font-medium text-slate-700 dark:text-slate-300">策略调整建议（Quant 复盘）</span>
                          {needChange ? (
                            <span className="text-[10px] px-1.5 py-0.5 rounded bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400">建议调整</span>
                          ) : (
                            <span className="text-[10px] px-1.5 py-0.5 rounded bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400">维持策略</span>
                          )}
                        </div>
                        <div className={`p-3 rounded-lg ${needChange ? 'bg-red-50 dark:bg-red-900/20 border border-red-100 dark:border-red-800' : 'bg-cyan-50 dark:bg-cyan-900/20 border border-cyan-100 dark:border-cyan-800'}`}>
                          <p className="text-xs text-slate-600 dark:text-slate-400 leading-relaxed mb-2">{suggestion.reason}</p>
                          {Array.isArray(suggestion.actions) && suggestion.actions.length > 0 && (
                            <div className="flex flex-wrap gap-1.5">
                              {suggestion.actions.map((a: string, i: number) => (
                                <span key={i} className="text-[10px] px-1.5 py-0.5 rounded bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 text-slate-600 dark:text-slate-400">
                                  {a}
                                </span>
                              ))}
                            </div>
                          )}
                        </div>
                      </div>
                    )
                  })()}

                  {/* Recommendations */}
                  <div>
                    <div className="flex items-center gap-2 mb-2">
                      <Lightbulb className="w-4 h-4 text-yellow-500" />
                      <span className="text-sm font-medium text-slate-700 dark:text-slate-300">投资建议</span>
                    </div>
                    <p className="text-sm text-slate-600 dark:text-slate-400 leading-relaxed p-3 rounded-lg bg-yellow-50 dark:bg-yellow-900/20">
                      {review.recommendations}
                    </p>
                  </div>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </div>
  )
}
