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
  Layers,
  Lightbulb,
} from 'lucide-react'
import {
  GetDailyReviews,
  GenerateDailyReview,
  RunCIODailyCheck,
  GetCIOStatus,
  GetCIOJournal,
  GetCurrentMarketState,
  GetPortfolioState,
  GetPortfolioRiskMetrics,
  GetMarketSixDimReports,
  GetDailyStrategyPlans,
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
  marketState: string
  marketConfidence: number
  date: string
  isFallback?: boolean
  optimization?: OptimizationResult | null
  sixdim?: MarketSixDim | null
  evidence?: DecisionProposalEvidence | null
  llmReview?: LLMReview | null
}

// 决策主张层 + 对抗评审证据链负载（对应后端 decisionProposalPayload）
interface DecisionProposalEvidence {
  market_direction?: string
  market_read?: string
  suggested_position_rate?: number
  effective_position_rate?: number
  governed_by_sixdim?: boolean
  preferred_stocks?: { code: string; reason: string }[]
  confidence?: number
  conclusion?: string
  skeptic_verdict?: string
  skeptic_concerns?: string[]
  feedback?: string
  evidence?: string[]
  generated_at?: string
}

// LLM 复议层输出（对应后端 llmReviewResult）
interface LLMReview {
  action?: string
  score?: number
  reason?: string
  suggestion?: string
}

interface OptimizationWeight {
  code: string
  name: string
  weight: number
}

interface OptimizationResult {
  strategy: string
  expected_return: number
  expected_volatility: number
  sharpe_ratio: number
  weights: OptimizationWeight[]
  constraints?: {
    max_industry_weight: number
    max_stocks_per_industry: number
  }
  industry_exposure?: IndustryExposure[]
  brinson?: BrinsonResult
}

interface IndustryExposure {
  industry: string
  weight: number
  limit: number
}

interface BrinsonIndustry {
  industry: string
  portfolio_weight: number
  benchmark_weight: number
  portfolio_return: number
  benchmark_return: number
  allocation: number
  selection: number
  interaction: number
}

interface BrinsonResult {
  benchmark_return: number
  portfolio_return: number
  excess_return: number
  allocation: number
  selection: number
  interaction: number
  industries: BrinsonIndustry[]
}

interface TodayDecision {
  decision: string
  reason: string
  riskApproval: string
  policyStatus: string
  marketState: string
  marketConfidence: number
  timestamp: string
  date?: string
  isFallback?: boolean
  optimization?: OptimizationResult | null
  sixdim?: MarketSixDim | null
}

// 市场六维判势结果（《六维策略.md》模块B MarketSixDim）
interface MarketSixDim {
  as_of?: string
  dim_scores?: Record<string, number>
  dim_chinese?: Record<string, string>
  raw_total_score?: number
  conflict_count?: number
  adjusted_total_score?: number
  position_rate?: number
  market_tag?: string
  position_advice?: string
  sources?: Record<string, string>
}

interface SixDimHistoryReport {
  trade_date: string
  dim_scores?: Record<string, number>
  raw_total_score?: number
  conflict_count?: number
  adjusted_total_score?: number
  position_rate?: number
  market_tag?: string
  sources?: Record<string, string>
}

// 策略日计划：量化分析师盘后选定次日交易策略，操盘手次日按该策略信号执行卖出（如 KDJ 死叉）
interface DailyStrategyPlan {
  id: number
  trade_date: string
  strategy_type: string
  strategy_name: string
  reason?: string
  status: string
  executed_sell_num: number
  created_by?: string
  source?: string
}

const sixDimOrder = ['tech', 'breadth', 'volume', 'capital', 'sentiment', 'external']
const sixDimDefaultChinese: Record<string, string> = {
  tech: '技术趋势',
  breadth: '市场广度',
  volume: '量能流动性',
  capital: '资金结构',
  sentiment: '情绪赚钱效应',
  external: '外部约束',
}

// 六维判势：每维度的数据源key + 金融含义 + 分值解读（《六维策略.md》模块B MarketSixDim）
// sourceKeys 对应后端 MarketSixDim.sources 返回的真实数据源标注键名
const sixDimDetail: Record<string, { sourceKeys: string[]; meaning: string; scoreNote: string }> = {
  tech: {
    sourceKeys: ['tech'],
    meaning: '大盘中期方向：以上证指数均线系统(MA20/MA60)和5日/20日涨跌幅衡量趋势多空',
    scoreNote: '≥70 多头排列、上涨动能强；45~70 中性震荡；<45 空头排列、下跌动能',
  },
  breadth: {
    sourceKeys: ['breadth_rise', 'breadth_highlow', 'breadth_limit', 'breadth_hotline'],
    meaning: '市场参与宽度：涨跌家数比、20日新高/新低数、涨停/跌停家数、热点板块集中度',
    scoreNote: '≥70 普涨、赚钱效应广；45~70 分化；<45 普跌、赚钱效应收窄',
  },
  volume: {
    sourceKeys: ['volume'],
    meaning: '量能流动性：全市场成交额相对20日均值，以及价格涨跌与成交量的配合情况',
    scoreNote: '量价齐升为强；缩量回调为中；放量下跌为弱',
  },
  capital: {
    sourceKeys: ['capital'],
    meaning: '资金结构：资金风险偏好与集中度（北向无实时源，以成交额连续放量天数代理）',
    scoreNote: '资金连续流入、集中度高为强；流出、涣散为弱',
  },
  sentiment: {
    sourceKeys: ['sentiment'],
    meaning: '短线情绪与赚钱效应：涨停/跌停家数、炸板率反映接力与惜售情绪',
    scoreNote: '涨停多、炸板率低为热；跌停多、炸板率高为冷',
  },
  external: {
    sourceKeys: ['external'],
    meaning: '隔夜外围与事件风险：无实时数据源，按中性处理、不猜测',
    scoreNote: '仅作提示维度，默认中性',
  },
}

const marketTagColor: Record<string, string> = {
  强势: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400',
  结构性震荡: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400',
  偏弱: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-400',
  退潮风险: 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400',
}

// 兼容后端两套风控取值：cio.go 落库用 APPROVED/REJECTED，agents/workflow.go 用 APPROVE/REJECT
const isRiskApproved = (v?: string) => v === 'APPROVED' || v === 'APPROVE'
const isRiskRejected = (v?: string) => v === 'REJECTED' || v === 'REJECT'

// 决策主张层方向 / 异议评审 / 复议动作 中文标签
const propDirectionCN = (v?: string) =>
  v === 'BULLISH' ? '看多' : v === 'BEARISH' ? '看空' : v === 'NEUTRAL' ? '中性/震荡' : (v || '--')
const skepticVerdictCN = (v?: string) =>
  v === 'AGREE' ? '认可' : v === 'CAUTION' ? '谨慎（可注意风险）' : v === 'RISK' ? '降仓（明显风险）' : (v || '--')
const reviewActionCN = (v?: string) =>
  v === 'APPROVE' ? '通过' : v === 'MODIFY' ? '修改' : v === 'REJECT' ? '否决' : (v || '--')

// 六维判势雷达图：6 个轴，0-100 映射到半径
function MarketRadar({ scores, chinese }: { scores: Record<string, number>; chinese: Record<string, string> }) {
  const keys = sixDimOrder
  const cx = 120
  const cy = 112
  const R = 78
  const pt = (i: number, r: number): [number, number] => {
    const a = (Math.PI * 2 * i) / keys.length - Math.PI / 2
    return [cx + r * Math.cos(a), cy + r * Math.sin(a)]
  }
  const rings = [0.25, 0.5, 0.75, 1]
  const poly = (list: [number, number][]) => list.map((p) => `${p[0]},${p[1]}`).join(' ')
  const valuePts = keys.map((k, i) => pt(i, ((scores[k] ?? 0) / 100) * R))
  return (
    <svg viewBox="0 0 240 224" className="w-full h-full max-h-72 text-slate-400 dark:text-slate-500">
      {rings.map((r) => (
        <polygon key={r} points={poly(keys.map((_, i) => pt(i, R * r)))} fill="none" stroke="currentColor" strokeWidth="1" opacity="0.3" />
      ))}
      {keys.map((k, i) => {
        const [x, y] = pt(i, R)
        return <line key={k} x1={cx} y1={cy} x2={x} y2={y} stroke="currentColor" strokeWidth="1" opacity="0.3" />
      })}
      <polygon points={poly(valuePts)} fill="rgba(99,102,241,0.30)" stroke="#6366f1" strokeWidth="1.5" strokeLinejoin="round" />
      {keys.map((k, i) => {
        const [x, y] = pt(i, R + 14)
        return (
          <text key={k} x={x} y={y} textAnchor="middle" dominantBaseline="middle"
            fill="currentColor" className="text-slate-500 dark:text-slate-400" style={{ fontSize: 11, fontWeight: 500 }}>
            {chinese[k] || k}
          </text>
        )
      })}
    </svg>
  )
}

const optimizationStrategyLabel: Record<string, string> = {
  EQUAL_WEIGHT: '等权',
  RISK_PARITY: '风险平价',
  MIN_VARIANCE: '最小方差',
  MAX_SHARPE: '最大夏普',
  MARKOWITZ: '马科维茨',
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
  const [reviews, setReviews] = useState<DailyReview[]>([])
  const [loadingReviews, setLoadingReviews] = useState(false)
  const [generatingReview, setGeneratingReview] = useState(false)
  const [activeTab, setActiveTab] = useState<'decision' | 'review'>('decision')
  const [loading, setLoading] = useState(true)
  const [currentMarketState, setCurrentMarketState] = useState<any>(null)
  const [riskMetrics, setRiskMetrics] = useState<any>(null)
  const [marketSixDimReports, setMarketSixDimReports] = useState<SixDimHistoryReport[]>([])
  const [dailyStrategyPlans, setDailyStrategyPlans] = useState<DailyStrategyPlan[]>([])

  // CIO 日志分页状态
  const [journalPage, setJournalPage] = useState(1)
  const journalPageSize = 5

  // 数据获取失败自动重试：标记去重
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const reviewNotifiedRef = useRef(false)

  const loadAll = useCallback(async (): Promise<boolean> => {
    setLoading(true)
    let anyFailed = false

    // Phase 1 —— CIO 运行状态（快）：仅读内存/DB 状态，立即结束页面级 loading。
    // 决策日志（CIO 日志 · 投资决策记录）、组合、市场状态、风险、六维、策略计划等卡片在 Phase 2 后台异步填充，避免页面加载很慢。
    try {
      const status = await GetCIOStatus()
      if (status) {
        setCioStatusState(status)
      }
    } catch {
      anyFailed = true
      showError('CIO状态加载失败', '无法获取CIO运行状态')
    }

    // CIO 状态就绪即结束页面级 loading；其余卡片在 Phase 2 后台异步填充
    setLoading(false)

    try {
      // Phase 2 —— 决策日志（CIO 日志 · 投资决策记录）独立先行加载：核心卡片立即显示，
      // 不被六维/风险等慢接口拖累（此前 Promise.allSettled 捆绑 6 接口，最慢的会阻塞日志渲染）。
      GetCIOJournal(7)
        .then((journalResult) => {
          if (Array.isArray(journalResult?.entries)) {
            setJournal(journalResult.entries as JournalEntry[])
          } else {
            anyFailed = true
            showWarning('日志加载异常', '无法获取CIO决策日志')
          }
        })
        .catch(() => {
          anyFailed = true
          showWarning('日志加载异常', '无法获取CIO决策日志')
        })

      // 其余卡片（组合/市场状态/风险/六维/策略）并行加载，互不阻塞决策日志渲染
      const [portfolioResult, marketStateResult, riskResult, sixDimResult, strategyPlanResult] = await Promise.allSettled([
        GetPortfolioState(),
        GetCurrentMarketState(),
        GetPortfolioRiskMetrics(),
        GetMarketSixDimReports(7),
        GetDailyStrategyPlans(5),
      ])

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

      if (sixDimResult.status === 'fulfilled' && Array.isArray(sixDimResult.value?.reports)) {
        setMarketSixDimReports(sixDimResult.value.reports as SixDimHistoryReport[])
      } else {
        anyFailed = true
        const sixDimErr = sixDimResult.status === 'rejected' ? sixDimResult.reason : sixDimResult.value
        console.warn('六维判势接口异常', sixDimErr)
        // 无兜底数据（决策日志中的 sixdim）时提示；有兜底则静默降级展示
        const hasFallback = journal.some((e) => e.sixdim && e.sixdim.dim_scores)
        if (!hasFallback) {
          showWarning('六维判势加载异常', '无法获取市场六维判势结果')
        }
      }

      if (strategyPlanResult.status === 'fulfilled' && Array.isArray(strategyPlanResult.value?.plans)) {
        setDailyStrategyPlans(strategyPlanResult.value.plans as DailyStrategyPlan[])
      } else {
        console.warn('策略日计划接口异常', strategyPlanResult.status === 'rejected' ? strategyPlanResult.reason : strategyPlanResult.value)
      }
    } catch (err) {
      anyFailed = true
      const errMsg = err instanceof Error ? err.message : String(err)
      showError('CIO数据加载失败', `加载过程出现异常: ${errMsg}`)
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
  // 风险评价是否具备真实数值字段；后端在历史收益不足时只返回 note（"数据不足"提示），
  // 此时不应渲染 N/A 网格，而应显示该说明。
  const hasRiskMetrics =
    riskMetrics != null &&
    typeof riskMetrics.var95_daily_return_pct === 'number' &&
    typeof riskMetrics.var99_daily_return_pct === 'number' &&
    typeof riskMetrics.cvar95_daily_return_pct === 'number' &&
    typeof riskMetrics.annualized_volatility_pct === 'number' &&
    typeof riskMetrics.max_drawdown_pct === 'number'
  // 最新一条决策记录（journal 按时间倒序），用于六维判势 / 组合优化 / 归因等卡片的数据源。
  // 决策记录统一来自「CIO 日志 · 投资决策记录」（decision_logs 表），不再单独维护今日决策快照。
  const todayDecision = journal.length > 0
    ? journal[0]
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
        date: '',
        isFallback: false,
        optimization: null,
        sixdim: null,
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
      {/* 市场六维判势（《六维策略.md》模块B MarketSixDim） */}
      {(() => {
        const sixDim = todayDecision?.sixdim ?? (marketSixDimReports.length > 0 ? (marketSixDimReports[0] as unknown as MarketSixDim) : null)
        if (!sixDim || !sixDim.dim_scores) return null
        const dimScores = sixDim.dim_scores
        const chinese = { ...sixDimDefaultChinese, ...(sixDim.dim_chinese || {}) }
        const adjusted = typeof sixDim.adjusted_total_score === 'number' ? sixDim.adjusted_total_score : null
        const positionRate = typeof sixDim.position_rate === 'number' ? sixDim.position_rate : null
        return (
          <div className="card p-5 mt-4">
            <div className="flex items-center gap-2 mb-4">
              <BarChart3 className="w-4 h-4 text-brand-500" />
              <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">市场六维判势</h2>
              <span className="text-xs text-slate-400 dark:text-slate-500 ml-auto">
                {sixDim.as_of ? `数据日期 ${sixDim.as_of}` : 'MarketSixDim'}
              </span>
            </div>

            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-4">
              <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-800/60">
                <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">市场标签</div>
                <span className={`inline-block text-sm font-semibold px-2 py-0.5 rounded ${marketTagColor[sixDim.market_tag || ''] || 'bg-slate-100 text-slate-600'}`}>
                  {sixDim.market_tag || '—'}
                </span>
              </div>
              <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-800/60">
                <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">仓位系数 position_rate</div>
                <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                  {positionRate !== null ? `× ${positionRate.toFixed(2)}` : '—'}
                </div>
              </div>
              <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-800/60">
                <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">修正总分</div>
                <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                  {adjusted !== null ? adjusted.toFixed(1) : '—'}
                  {typeof sixDim.raw_total_score === 'number' && (
                    <span className="text-xs text-slate-400 font-normal ml-1">原始 {sixDim.raw_total_score.toFixed(1)}</span>
                  )}
                </div>
              </div>
              <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-800/60">
                <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">矛盾维度</div>
                <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                  {typeof sixDim.conflict_count === 'number' ? `${sixDim.conflict_count} 个` : '—'}
                </div>
              </div>
            </div>

            <div className="flex flex-col lg:flex-row gap-5 mb-4">
              <div className="lg:w-2/5 min-h-56 text-slate-600 dark:text-slate-400">
                <MarketRadar scores={dimScores} chinese={chinese} />
              </div>
              <div className="flex-1 space-y-3">
                {sixDimOrder.map((key) => {
                  const score = dimScores[key]
                  if (typeof score !== 'number') return null
                  const detail = sixDimDetail[key]
                  const srcText = (detail?.sourceKeys ?? [])
                    .map((sk) => (sixDim.sources as Record<string, string> | undefined)?.[sk])
                    .filter((v): v is string => !!v && v.trim() !== '')
                    .join('；')
                  return (
                    <div key={key} className="flex items-center gap-3 text-sm">
                      <span className="w-16 shrink-0 text-slate-500 dark:text-slate-400 text-xs">{chinese[key] || key}</span>
                      <div className="h-1.5 flex-1 rounded-full bg-slate-100 dark:bg-slate-700 overflow-hidden" title={srcText}>
                        <div
                          className={`h-full rounded-full ${score >= 70 ? 'bg-emerald-500' : score >= 45 ? 'bg-amber-500' : 'bg-red-500'}`}
                          style={{ width: `${Math.min(100, Math.max(2, score))}%` }}
                        />
                      </div>
                      <span className="w-8 shrink-0 text-right font-semibold text-slate-800 dark:text-slate-100">{score.toFixed(0)}</span>
                      <span className="flex-1 min-w-0 text-xs text-slate-400 dark:text-slate-500 truncate" title={`${detail?.scoreNote ?? ''}${srcText ? `；来源：${srcText}` : ''}`}>
                        {detail?.scoreNote}
                      </span>
                    </div>
                  )
                })}
              </div>
            </div>

            {sixDim.position_advice && (
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-2">{sixDim.position_advice}</div>
            )}
          </div>
        )
      })()}

      {/* 今日交易策略（策略日计划：量化分析师盘后选定，操盘手次日按信号执行） */}
      {dailyStrategyPlans.length > 0 && (
        <div className="card p-5 mt-4">
          <div className="flex items-center gap-2 mb-4">
            <TrendingUp className="w-4 h-4 text-brand-500" />
            <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">今日交易策略</h2>
            <span className="text-xs text-slate-400 dark:text-slate-500 ml-auto">操盘手按策略信号执行卖出（如 KDJ 死叉）</span>
          </div>
          {dailyStrategyPlans.map((p, idx) => (
            <div key={p.id} className={`${idx > 0 ? 'mt-3 pt-3 border-t border-slate-100 dark:border-slate-700' : ''}`}>
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-xs font-medium text-slate-500 dark:text-slate-400">{p.trade_date} 计划</span>
                <span className="inline-block text-sm font-semibold px-2 py-0.5 rounded bg-brand-50 dark:bg-brand-500/10 text-brand-600 dark:text-brand-400">
                  {p.strategy_name}
                </span>
                <span className="inline-block text-xs px-2 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-slate-500 dark:text-slate-300">
                  {p.strategy_type}
                </span>
                <span className={`text-xs px-2 py-0.5 rounded ${p.source === 'quant' ? 'bg-emerald-50 dark:bg-emerald-500/10 text-emerald-600 dark:text-emerald-400' : 'bg-slate-100 dark:bg-slate-700 text-slate-500 dark:text-slate-300'}`}>
                  {p.source === 'quant' ? '量化分析师选定' : '盘后自动兑底'}
                </span>
                <span className="text-xs text-slate-400 dark:text-slate-500 ml-auto">已执行卖出信号 {p.executed_sell_num ?? 0} 次</span>
              </div>
              {p.reason && (
                <p className="mt-2 text-xs text-slate-500 dark:text-slate-400 leading-relaxed">{p.reason}</p>
              )}
            </div>
          ))}
        </div>
      )}

      {/* 组合优化结果（真实协方差 · 均值-方差/风险平价） */}
      {todayDecision?.optimization && todayDecision.optimization.weights && todayDecision.optimization.weights.length > 0 && (
        <div className="card p-5 mt-4">
          <div className="flex items-center gap-2 mb-4">
            <Layers className="w-4 h-4 text-brand-500" />
            <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">组合优化结果</h2>
            <span className="text-xs text-slate-400 dark:text-slate-500 ml-auto">真实协方差 · 因子评分作预期收益</span>
          </div>

          <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-4">
            <div className="p-3 rounded-lg bg-slate-50 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">优化策略</div>
              <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                {optimizationStrategyLabel[todayDecision.optimization.strategy] || todayDecision.optimization.strategy}
              </div>
            </div>
            <div className="p-3 rounded-lg bg-slate-50 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">组合预期收益</div>
              <div className="text-sm font-semibold text-emerald-600 dark:text-emerald-400">
                {typeof todayDecision.optimization.expected_return === 'number' ? `${todayDecision.optimization.expected_return.toFixed(2)}%` : '—'}
              </div>
            </div>
            <div className="p-3 rounded-lg bg-slate-50 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">组合预期波动</div>
              <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                {typeof todayDecision.optimization.expected_volatility === 'number' ? `${todayDecision.optimization.expected_volatility.toFixed(2)}%` : '—'}
              </div>
            </div>
            <div className="p-3 rounded-lg bg-slate-50 dark:border-slate-700">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">夏普比率</div>
              <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                {typeof todayDecision.optimization.sharpe_ratio === 'number' ? todayDecision.optimization.sharpe_ratio.toFixed(2) : '—'}
              </div>
            </div>
          </div>

          <div className="text-xs text-slate-400 dark:text-slate-500 mb-2">目标权重分配</div>
          <div className="space-y-2">
            {todayDecision.optimization.weights.map((item) => (
              <div key={item.code} className="flex items-center gap-3">
                <span className="w-28 truncate text-xs text-slate-600 dark:text-slate-300">{item.name || item.code}</span>
                <div className="flex-1 h-2 rounded bg-slate-100 dark:bg-slate-800 overflow-hidden">
                  <div
                    className="h-full rounded bg-gradient-to-r from-brand-500 to-cyan-400"
                    style={{ width: `${Math.min(100, Math.max(0, item.weight))}%` }}
                  />
                </div>
                <span className="w-14 text-right text-xs font-semibold text-slate-800 dark:text-slate-100">
                  {item.weight.toFixed(1)}%
                </span>
              </div>
            ))}
          </div>

          {todayDecision.optimization.constraints && (
            <div className="mt-3 flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
              <Shield className="w-3.5 h-3.5 text-brand-500" />
              <span>行业风控约束：单一行业敞口 ≤ {(todayDecision.optimization.constraints.max_industry_weight * 100).toFixed(0)}%，同行业最多 {todayDecision.optimization.constraints.max_stocks_per_industry} 只</span>
            </div>
          )}
        </div>
      )}

      {/* 行业敞口 */}
      {todayDecision?.optimization?.industry_exposure && todayDecision.optimization.industry_exposure.length > 0 && (
        <div className="card p-5 mt-4">
          <div className="flex items-center gap-2 mb-4">
            <Target className="w-4 h-4 text-brand-500" />
            <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">行业敞口</h2>
            <span className="text-xs text-slate-400 dark:text-slate-500 ml-auto">单一行业上限 30%</span>
          </div>
          <div className="grid grid-cols-2 lg:grid-cols-3 gap-3">
            {todayDecision.optimization.industry_exposure.map((ind) => (
              <div key={ind.industry} className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                <div className="flex items-baseline justify-between mb-2">
                  <span className="text-xs text-slate-600 dark:text-slate-300 truncate">{ind.industry}</span>
                  <span className={`text-xs font-semibold ${ind.weight > ind.limit ? 'text-red-500' : 'text-slate-700 dark:text-slate-200'}`}>
                    {ind.weight.toFixed(1)}%
                  </span>
                </div>
                <div className="h-1.5 rounded bg-slate-100 dark:bg-slate-800 overflow-hidden">
                  <div
                    className={`h-full rounded ${ind.weight > ind.limit ? 'bg-red-500' : 'bg-gradient-to-r from-brand-500 to-cyan-400'}`}
                    style={{ width: `${Math.min(100, Math.max(0, (ind.weight / ind.limit) * 100))}%` }}
                  />
                </div>
                <div className="mt-1 text-right text-[10px] text-slate-400 dark:text-slate-500">上限 {ind.limit.toFixed(0)}%</div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Brinson 行业归因 */}
      {todayDecision?.optimization?.brinson && todayDecision.optimization.brinson.industries.length > 0 && (
        <div className="card p-5 mt-4">
          <div className="flex items-center gap-2 mb-4">
            <Lightbulb className="w-4 h-4 text-brand-500" />
            <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">Brinson 行业归因</h2>
            <span className="text-xs text-slate-400 dark:text-slate-500 ml-auto">组合 vs 等权全候选池 · 区间真实收益</span>
          </div>

          <div className="grid grid-cols-2 sm:grid-cols-3 gap-3 mb-4">
            <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">组合超额收益</div>
              <div className={`text-sm font-semibold ${todayDecision.optimization.brinson.excess_return >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-500'}`}>
                {(todayDecision.optimization.brinson.excess_return >= 0 ? '+' : '') + todayDecision.optimization.brinson.excess_return.toFixed(2)}%
              </div>
            </div>
            <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">配置效应</div>
              <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                {(todayDecision.optimization.brinson.allocation >= 0 ? '+' : '') + todayDecision.optimization.brinson.allocation.toFixed(2)}%
              </div>
            </div>
            <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-900/50">
              <div className="text-xs text-slate-500 dark:text-slate-400 mb-1">选择效应</div>
              <div className="text-sm font-semibold text-slate-800 dark:text-slate-100">
                {(todayDecision.optimization.brinson.selection >= 0 ? '+' : '') + todayDecision.optimization.brinson.selection.toFixed(2)}%
              </div>
            </div>
          </div>

          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="text-left text-slate-400 dark:text-slate-500 border-b border-slate-100 dark:border-slate-700">
                  <th className="py-2 pr-2 font-medium">行业</th>
                  <th className="py-2 pr-2 font-medium">组合权重</th>
                  <th className="py-2 pr-2 font-medium">组合收益</th>
                  <th className="py-2 pr-2 font-medium">配置效应</th>
                  <th className="py-2 pr-2 font-medium">选择效应</th>
                  <th className="py-2 pr-2 font-medium">交互效应</th>
                </tr>
              </thead>
              <tbody>
                {todayDecision.optimization.brinson.industries.map((row) => (
                  <tr key={row.industry} className="border-b border-slate-50 dark:border-slate-800">
                    <td className="py-2 pr-2 text-slate-600 dark:text-slate-300">{row.industry}</td>
                    <td className="py-2 pr-2 text-slate-500 dark:text-slate-400">{row.portfolio_weight.toFixed(1)}%</td>
                    <td className={`py-2 pr-2 font-medium ${row.portfolio_return >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-500'}`}>
                      {(row.portfolio_return >= 0 ? '+' : '') + row.portfolio_return.toFixed(2)}%
                    </td>
                    <td className={`py-2 pr-2 ${row.allocation >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-500'}`}>
                      {(row.allocation >= 0 ? '+' : '') + row.allocation.toFixed(2)}%
                    </td>
                    <td className={`py-2 pr-2 ${row.selection >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-500'}`}>
                      {(row.selection >= 0 ? '+' : '') + row.selection.toFixed(2)}%
                    </td>
                    <td className={`py-2 pr-2 ${row.interaction >= 0 ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-500'}`}>
                      {(row.interaction >= 0 ? '+' : '') + row.interaction.toFixed(2)}%
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {/* 风险评价（VaR / ES）及含义 */}
      <div className="card p-5">
        <div className="flex items-center gap-2 mb-4">
          <BarChart3 className="w-4 h-4 text-brand-500" />
          <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">风险评价 · VaR / ES</h2>
          <span className="text-xs text-slate-400 dark:text-slate-500 ml-auto">历史法 · 基于真实组合结算收益</span>
        </div>

        {hasRiskMetrics ? (
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
              {typeof riskMetrics?.note === 'string' && riskMetrics.note.length > 0
                ? riskMetrics.note
                : '暂无风险评价数据'}
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
                        isRiskApproved(entry.riskApproval) ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' :
                        isRiskRejected(entry.riskApproval) ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400' :
                        'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
                      }`}>
                        {isRiskApproved(entry.riskApproval) ? '✓ 通过' : isRiskRejected(entry.riskApproval) ? '✗ 否决' : '审核中'}
                      </span>
                    )}
                  </div>
                  <p className="text-xs text-slate-600 dark:text-slate-400 leading-relaxed">{entry.reason}</p>

                  {/* 决策主张层 + 对抗评审 + 复议 */}
                  {(entry.evidence?.market_direction || entry.evidence?.skeptic_verdict || entry.llmReview) && (
                    <div className="mt-1.5 space-y-1 border-t border-slate-200 dark:border-slate-800 pt-1.5">
                      {entry.evidence?.market_direction && (
                        <div className="text-[11px] text-slate-600 dark:text-slate-400 leading-relaxed">
                          <span className="font-medium text-amber-600 dark:text-amber-400">【LLM主张】</span>
                          方向 {propDirectionCN(entry.evidence.market_direction)}
                          {entry.evidence.market_read ? ` · ${entry.evidence.market_read}` : ''}
                          {typeof entry.evidence.confidence === 'number' && ` · 信心 ${entry.evidence.confidence}%`}
                          {typeof entry.evidence.suggested_position_rate === 'number' && (
                            ` · 主张仓位 ${(entry.evidence.suggested_position_rate * 100).toFixed(0)}%`
                          )}
                          {typeof entry.evidence.effective_position_rate === 'number' && (
                            ` · 生效仓位 ${(entry.evidence.effective_position_rate * 100).toFixed(0)}%${typeof entry.evidence.suggested_position_rate === 'number' && entry.evidence.effective_position_rate < entry.evidence.suggested_position_rate ? '（已趋保守）' : ''}`
                          )}
                          {entry.evidence.preferred_stocks && entry.evidence.preferred_stocks.length > 0 && (
                            ` · 偏好 ${entry.evidence.preferred_stocks.map((p) => p.code).join('、')}`
                          )}
                          {entry.evidence.feedback && ` · 归因: ${entry.evidence.feedback}`}
                        </div>
                      )}
                      {entry.evidence?.skeptic_verdict && (
                        <div className="text-[11px] text-slate-600 dark:text-slate-400 leading-relaxed">
                          <span className="font-medium text-rose-600 dark:text-rose-400">【异议评审】</span>
                          {skepticVerdictCN(entry.evidence.skeptic_verdict)}
                          {entry.evidence.skeptic_concerns && entry.evidence.skeptic_concerns.length > 0
                            ? ` · ${entry.evidence.skeptic_concerns.join('；')}`
                            : ''}
                        </div>
                      )}
                      {entry.llmReview && (
                        <div className="text-[11px] text-slate-600 dark:text-slate-400 leading-relaxed">
                          <span className="font-medium text-blue-600 dark:text-blue-400">【LLM复议】</span>
                          {reviewActionCN(entry.llmReview.action)}（{entry.llmReview.score}/100）
                          {entry.llmReview.reason ? ` · ${entry.llmReview.reason}` : ''}
                          {entry.llmReview.suggestion ? ` · 建议: ${entry.llmReview.suggestion}` : ''}
                        </div>
                      )}
                    </div>
                  )}
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
