import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { useViewModeStore } from '../store/viewModeStore'
import {
  TrendingUp,
  TrendingDown,
  Shield,
  Brain,
  Eye,
  PanelLeft,
  Play,
  Square,
  Sparkles,
  DollarSign,
  PiggyBank,
  Activity,
  AlertTriangle,
  Loader2,
  PieChart,
  Target,
  RefreshCw,
} from 'lucide-react'
import { GetPortfolioState, RefreshPrices as RefreshPricesAPI, GetPolicyStatus, EmergencyStop, ResumeTrading, GetCIOJournal, IsReady } from '../../wailsjs/go/main/App'
import { formatCurrency, formatPercent, getPnLColor } from '../utils/formatters'
import { toFriendlyError } from '../utils/errorMessages'
import { useToastStore } from '../store/toastStore'

interface Position {
  code: string
  stockName: string
  quantity: number
  currentPrice: number
  marketValue: number
  weight: number
  unrealizedPnL: number
  unrealizedReturn: number
}

interface AIDecision {
  decisionID: string
  decision: string
  reason: string
  riskApproval: string
  policyStatus: string
  marketState: string
  marketConfidence: number
  timestamp: string
  orders: Array<{ code: string; side: string; quantity: number; reason?: string }>
}

interface PortfolioState {
  portfolioID: number
  totalCapital: number
  totalAssets: number
  cash: number
  totalMarketValue: number
  totalPnL: number
  totalReturn: number
  dailyPnL: number
  dailyReturn: number
  positionsCount: number
  positions: Position[]
  lastUpdated: string
}

export default function Dashboard() {
  const { mode, toggleMode } = useViewModeStore()
  const [isStopped, setIsStopped] = useState(false)
  const [portfolio, setPortfolio] = useState<PortfolioState | null>(null)
  const [decisions, setDecisions] = useState<AIDecision[]>([])
  const [decisionsLoading, setDecisionsLoading] = useState(true)
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [runMessage, setRunMessage] = useState<{ type: 'success' | 'error' | 'warning' | 'info'; text: string } | null>(null)
  const [holdingsPage, setHoldingsPage] = useState(0)

  // 数据获取失败弹窗去重标记（成功后重置）
  const failNotifiedRef = useRef(false)

  // 等待后端就绪
  const waitForReady = useCallback(async (maxRetries: number = 30): Promise<{ready: boolean, portfolioReady: boolean, status?: any}> => {
    for (let i = 0; i < maxRetries; i++) {
      try {
        const status = await IsReady()
        if (status?.ready) {
          return {ready: true, portfolioReady: status?.portfolio_ready || false, status}
        }
        // 显示进度
        const components = []
        if (status?.config_ready) components.push('配置')
        if (status?.sqlite_ready) components.push('数据库')
        if (status?.duckdb_ready) components.push('DuckDB')
        if (status?.portfolio_ready) components.push('组合引擎')
        setRunMessage({
          type: 'info',
          text: `系统初始化中... (${i + 1}/${maxRetries}) 已就绪: ${components.join(', ') || '无'}`,
        })
      } catch {
        // API 调用失败，继续等待
        setRunMessage({
          type: 'info',
          text: `正在连接后端服务... (${i + 1}/${maxRetries})`,
        })
      }
      await new Promise(resolve => setTimeout(resolve, 1000))
    }
    return {ready: false, portfolioReady: false}
  }, [])

  // 等待组合引擎就绪（系统可能因降级模式先就绪，组合引擎随后才初始化）
  const waitForPortfolio = useCallback(async (maxRetries: number = 30): Promise<boolean> => {
    for (let i = 0; i < maxRetries; i++) {
      try {
        const status = await IsReady()
        if (status?.portfolio_ready) {
          return true
        }
      } catch {
        // API 调用失败，继续等待
      }
      setRunMessage({
        type: 'info',
        text: `组合引擎初始化中... (${i + 1}/${maxRetries})`,
      })
      await new Promise(resolve => setTimeout(resolve, 1000))
    }
    return false
  }, [])

  const loadData = useCallback(async () => {
    setLoading(true)
    setRefreshing(true)
    try {
      // 先等待后端就绪
      const {ready, portfolioReady} = await waitForReady(30)
      if (!ready) {
        setRunMessage({
          type: 'error',
          text: '系统初始化超时，请重启应用或查看日志',
        })
        return
      }

      // 组合引擎可能因降级模式延迟初始化，等待其就绪后再加载数据
      if (!portfolioReady) {
        const ok = await waitForPortfolio(30)
        if (!ok) {
          setRunMessage({
            type: 'error',
            text: '投资组合引擎初始化超时，请重启应用或查看日志',
          })
          return
        }
      }

      const state = await GetPortfolioState()
      setPortfolio(state)
      setRunMessage(null)
      failNotifiedRef.current = false
    } catch (err) {
      console.error('Failed to load portfolio:', err)
      const friendly = toFriendlyError(err)
      setRunMessage({
        type: friendly.level === 'error' ? 'error' : 'warning',
        text: friendly.message,
      })
      // 数据获取失败：弹窗警示（已通过定时轮询自动重试）
      if (!failNotifiedRef.current) {
        failNotifiedRef.current = true
        useToastStore.getState().error('数据获取失败，将自动重试', friendly.message)
      }
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [waitForReady, waitForPortfolio])

  const loadContinuousStatus = useCallback(async () => {
    try {
      // 驾驶舱状态只与「紧急停止/恢复工作」开关关联
      const status = await GetPolicyStatus()
      if (status?.emergency_stop !== undefined) {
        setIsStopped(!!status.emergency_stop)
      }
    } catch {
      // ignore
    }
  }, [])

  const loadDecisions = useCallback(async () => {
    try {
      const result = await GetCIOJournal(7)
      setDecisions(result?.entries || [])
    } catch {
      setDecisions([])
    } finally {
      setDecisionsLoading(false)
    }
  }, [])

  useEffect(() => {
    loadData()
    loadContinuousStatus()
    loadDecisions()
  }, [loadData, loadContinuousStatus, loadDecisions])

  useEffect(() => {
    const interval = setInterval(async () => {
      try {
        await RefreshPricesAPI()
        loadData()
        loadDecisions()
      } catch {
        // ignore
      }
    }, 300000)
    return () => clearInterval(interval)
  }, [loadData, loadDecisions])

  const cioStatus = {
    state: 'NORMAL',
    todayMessage: isStopped ? '交易系统已停止' : 'AI 投资团队运行正常',
    teamMessage: 'AI 投资团队运行正常',
  }

  const handleEmergencyStop = async () => {
    try {
      await EmergencyStop()
      setIsStopped(true)
      setRunMessage({ type: 'success', text: '已紧急停止，所有智能体已停止工作' })
      setTimeout(() => setRunMessage(null), 3000)
    } catch (err) {
      const friendly = toFriendlyError(err)
      setRunMessage({ type: 'error', text: friendly.message })
      setTimeout(() => setRunMessage(null), 4000)
    }
  }

  const handleResume = async () => {
    try {
      await ResumeTrading()
      setIsStopped(false)
      setRunMessage({ type: 'success', text: '已恢复工作，所有智能体继续运行' })
      setTimeout(() => setRunMessage(null), 3000)
    } catch (err) {
      const friendly = toFriendlyError(err)
      setRunMessage({ type: 'error', text: friendly.message })
      setTimeout(() => setRunMessage(null), 4000)
    }
  }

  const totalAsset = portfolio?.totalAssets || 0
  const todayPnL = portfolio?.dailyPnL || 0
  const todayPnLPct = portfolio?.dailyReturn || 0
  const totalReturn = portfolio?.totalReturn || 0
  const holdings = portfolio?.totalMarketValue || 0
  const cash = portfolio?.cash || 0
  const positions = portfolio?.positions || []
  const positionsCount = portfolio?.positionsCount || 0

  // 持仓分页逻辑
  const HOLDINGS_PAGE_SIZE = 5
  const holdingsTotalPages = Math.ceil(positions.length / HOLDINGS_PAGE_SIZE)
  const safeHoldingsPage = Math.min(holdingsPage, holdingsTotalPages - 1)
  const pagePositions = useMemo(() => {
    const start = safeHoldingsPage * HOLDINGS_PAGE_SIZE
    return positions.slice(start, start + HOLDINGS_PAGE_SIZE)
  }, [positions, safeHoldingsPage])

  if (loading && !portfolio) {
    return (
      <div className="flex items-center justify-center h-64">
        <Loader2 className="w-8 h-8 animate-spin text-brand-500" />
      </div>
    )
  }

  const messageBanner = runMessage && (
    <div className={`fixed top-4 right-4 z-50 px-4 py-3 rounded-lg shadow-lg flex items-center gap-2 ${
      runMessage.type === 'error' ? 'bg-red-500 text-white'
      : runMessage.type === 'warning' ? 'bg-yellow-500 text-white'
      : runMessage.type === 'info' ? 'bg-blue-500 text-white'
      : 'bg-green-500 text-white'
    }`}>
      {runMessage.type === 'error' ? (
        <AlertTriangle className="w-4 h-4" />
      ) : runMessage.type === 'info' ? (
        <Loader2 className="w-4 h-4 animate-spin" />
      ) : (
        <TrendingUp className="w-4 h-4" />
      )}
      <span className="text-sm font-medium">{runMessage.text}</span>
    </div>
  )

  if (mode === 'simple') {
    return (
      <>
        {messageBanner}
        <SimpleMode
          totalAsset={totalAsset}
          todayPnL={todayPnL}
          todayPnLPct={todayPnLPct}
          totalReturn={totalReturn}
          holdings={holdings}
          cash={cash}
          positionsCount={positionsCount}
          cioStatus={cioStatus}
          isStopped={isStopped}
          onToggleMode={toggleMode}
          onEmergencyStop={handleEmergencyStop}
          onResume={handleResume}
          refreshing={refreshing}
        />
      </>
    )
  }

  return (
    <>
      {messageBanner}
        <DetailedMode
        totalAsset={totalAsset}
        todayPnL={todayPnL}
        todayPnLPct={todayPnLPct}
        totalReturn={totalReturn}
        holdings={holdings}
        cash={cash}
        positions={positions}
        positionsCount={positionsCount}
        decisions={decisions}
        decisionsLoading={decisionsLoading}
        cioStatus={cioStatus}
        isStopped={isStopped}
        onToggleMode={toggleMode}
        refreshing={refreshing}
        onRefresh={loadData}
        holdingsTotalPages={holdingsTotalPages}
        safeHoldingsPage={safeHoldingsPage}
        pagePositions={pagePositions}
        onHoldingsPageChange={setHoldingsPage}
      />
    </>
  )
}

interface SimpleModeProps {
  totalAsset: number
  todayPnL: number
  todayPnLPct: number
  totalReturn: number
  holdings: number
  cash: number
  positionsCount: number
  cioStatus: { state: string; todayMessage: string; teamMessage: string }
  isStopped: boolean
  onToggleMode: () => void
  onEmergencyStop: () => void
  onResume: () => void
  refreshing: boolean
}

function SimpleMode({
  totalAsset, todayPnL, todayPnLPct, totalReturn,
  holdings, cash, positionsCount, cioStatus,
  isStopped, onToggleMode, onEmergencyStop, onResume, refreshing,
}: SimpleModeProps) {
  return (
    <div className="min-h-[calc(100vh-80px)] flex flex-col">
      <header className="flex items-center justify-between mb-8">
        <div className="flex items-center gap-3">
          <div className="w-9 h-9 rounded-lg bg-gradient-to-br from-brand-500 to-cyan-400 flex items-center justify-center shadow-sm">
            <Brain className="w-4 h-4 text-white" />
          </div>
          <h1 className="text-lg font-bold text-slate-800 dark:text-slate-100">QuantBot</h1>
        </div>
        <div className="flex items-center gap-3">
          {isStopped ? (
            <div className="flex items-center gap-2 px-3 py-1.5 rounded-full bg-yellow-50 dark:bg-yellow-900/30">
              <span className="w-2 h-2 rounded-full bg-yellow-500" />
              <span className="text-xs font-medium text-yellow-700 dark:text-yellow-400">停止</span>
            </div>
          ) : (
            <div className="flex items-center gap-2 px-3 py-1.5 rounded-full bg-green-50 dark:bg-green-900/30">
              <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
              <span className="text-xs font-medium text-green-700 dark:text-green-400">正常</span>
            </div>
          )}
          {refreshing && <Loader2 className="w-4 h-4 animate-spin text-slate-400" />}
          <button onClick={onToggleMode} className="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400 hover:text-brand-600 dark:hover:text-brand-400 transition-colors px-2.5 py-1.5 rounded-md hover:bg-slate-100 dark:hover:bg-slate-800">
            <Eye className="w-3.5 h-3.5" />详细
          </button>
        </div>
      </header>

      <div className="flex-1 flex flex-col items-center justify-center">
        <div className="text-center mb-2">
          <p className="text-xs text-slate-400 dark:text-slate-500 mb-2 tracking-wider">总资产</p>
          <h2 className="text-5xl font-bold text-slate-800 dark:text-slate-100 tracking-tight">
            {formatCurrency(totalAsset)}
          </h2>
        </div>

        <div className="text-center mb-2">
          <div className="flex items-center justify-center gap-3">
            <span className={`text-2xl font-semibold ${getPnLColor(todayPnL)}`}>
              {todayPnL >= 0 ? '+' : ''}{formatCurrency(todayPnL)}
            </span>
            <span className={`text-lg font-medium ${getPnLColor(todayPnLPct)}`}>
              {formatPercent(todayPnLPct)}
            </span>
          </div>
          <p className="mt-1 text-sm text-slate-500 dark:text-slate-400">
            累计 {formatPercent(totalReturn)}
          </p>
        </div>

        <div className="my-6 py-4 w-full max-w-md">
          <div className="flex items-center justify-center gap-2 mb-2">
            <Shield className="w-5 h-5 text-green-500" />
            <span className="text-sm font-medium text-slate-700 dark:text-slate-300">一切正常</span>
          </div>
          <p className="text-sm text-slate-500 dark:text-slate-400 leading-relaxed text-center">
            系统正在自动管理你的投资
          </p>
        </div>

        <div className="w-full max-w-md">
          <div className="bg-gradient-to-r from-brand-50 to-cyan-50 dark:from-brand-900/20 dark:to-cyan-900/20 rounded-xl p-4 border border-brand-100 dark:border-brand-800 text-center">
            <div className="flex items-center justify-center gap-2 mb-2">
              <Sparkles className="w-4 h-4 text-brand-500" />
              <span className="text-sm font-semibold text-slate-700 dark:text-slate-300">AI 投资团队</span>
            </div>
            <p className="text-sm text-slate-600 dark:text-slate-400 leading-relaxed">
              {isStopped ? '交易系统已停止。点击「恢复工作」继续运行。' : cioStatus.todayMessage}
            </p>
          </div>
        </div>

        <div className="grid grid-cols-3 gap-4 w-full max-w-md mt-6">
          <div className="text-center">
            <p className="text-xs text-slate-400 dark:text-slate-500 mb-1">持仓市值</p>
            <p className="text-lg font-bold text-slate-800 dark:text-slate-100">{formatCurrency(holdings)}</p>
          </div>
          <div className="text-center">
            <p className="text-xs text-slate-400 dark:text-slate-500 mb-1">现金</p>
            <p className="text-lg font-bold text-slate-800 dark:text-slate-100">{formatCurrency(cash)}</p>
          </div>
          <div className="text-center">
            <p className="text-xs text-slate-400 dark:text-slate-500 mb-1">持仓数</p>
            <p className="text-lg font-bold text-slate-800 dark:text-slate-100">{positionsCount}</p>
          </div>
        </div>

        <div className="flex items-center gap-3 mt-8">
          {isStopped ? (
            <button onClick={onResume} className="flex items-center gap-2 px-4 py-2 bg-green-500 text-white rounded-lg hover:bg-green-600 transition-colors text-sm font-medium">
              <Play className="w-4 h-4" />恢复工作
            </button>
          ) : (
            <button onClick={onEmergencyStop} className="flex items-center gap-2 px-4 py-2 bg-red-500 text-white rounded-lg hover:bg-red-600 transition-colors text-sm font-medium">
              <Square className="w-4 h-4" />紧急停止
            </button>
          )}
        </div>
      </div>

      <footer className="text-center py-4 text-xs text-slate-400 dark:text-slate-500">
        {isStopped ? '交易已停止 · AI 投资团队待命' : '你的 AI 投资团队正在工作'}
      </footer>
    </div>
  )
}

interface DetailedModeProps {
  totalAsset: number
  todayPnL: number
  todayPnLPct: number
  totalReturn: number
  holdings: number
  cash: number
  positions: Position[]
  positionsCount: number
  decisions: AIDecision[]
  decisionsLoading: boolean
  cioStatus: { state: string; todayMessage: string; teamMessage: string }
  isStopped: boolean
  onToggleMode: () => void
  refreshing: boolean
  onRefresh: () => void
  // 持仓分页
  holdingsTotalPages: number
  safeHoldingsPage: number
  pagePositions: Position[]
  onHoldingsPageChange: (page: number) => void
}

function DetailedMode({
  totalAsset, todayPnL, todayPnLPct, totalReturn,
  holdings, cash, positions, positionsCount,
  decisions, decisionsLoading,
  cioStatus, isStopped, onToggleMode, refreshing, onRefresh,
  holdingsTotalPages, safeHoldingsPage, pagePositions, onHoldingsPageChange,
}: DetailedModeProps) {
  const teamStatus = [
    { role: 'CIO', name: '首席投资官', state: 'NORMAL', color: 'text-green-500', icon: Shield },
    { role: 'QUANT', name: '量化分析师', state: 'NORMAL', color: 'text-blue-500', icon: Brain },
    { role: 'RISK', name: '风控师', state: 'NORMAL', color: 'text-green-500', icon: Shield },
    { role: 'TRADER', name: '操盘手', state: 'IDLE', color: 'text-slate-500', icon: Activity },
  ]

  // 计算风险指标
  // 注意: weight字段已经是百分比值(如31.298表示31.298%)，直接使用即可
  const concentration = positions.length > 0
    ? Math.max(...positions.map(p => Math.abs(p.weight)))
    : 0
  const topHolding = positions.length > 0
    ? positions.reduce((a, b) => Math.abs(a.weight) > Math.abs(b.weight) ? a : b)
    : null
  const totalPnL = positions.reduce((sum, p) => sum + p.unrealizedPnL, 0)

  return (
    <div className="space-y-5">
      <header className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-9 h-9 rounded-lg bg-gradient-to-br from-brand-500 to-cyan-400 flex items-center justify-center shadow-sm">
            <Brain className="w-4 h-4 text-white" />
          </div>
          <h1 className="text-lg font-bold text-slate-800 dark:text-slate-100">QuantBot 驾驶舱</h1>
        </div>
        <div className="flex items-center gap-3">
          {isStopped ? (
            <div className="flex items-center gap-2 px-3 py-1.5 rounded-full bg-yellow-50 dark:bg-yellow-900/30">
              <span className="w-2 h-2 rounded-full bg-yellow-500" />
              <span className="text-xs font-medium text-yellow-700 dark:text-yellow-400">停止</span>
            </div>
          ) : (
            <div className="flex items-center gap-2 px-3 py-1.5 rounded-full bg-green-50 dark:bg-green-900/30">
              <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
              <span className="text-xs font-medium text-green-700 dark:text-green-400">正常</span>
            </div>
          )}
          <button onClick={onRefresh} className="p-1.5 rounded-md hover:bg-slate-100 dark:hover:bg-slate-800 transition-colors">
            <RefreshCw className={`w-4 h-4 text-slate-500 ${refreshing ? 'animate-spin' : ''}`} />
          </button>
          <button onClick={onToggleMode} className="flex items-center gap-1.5 text-xs text-slate-500 dark:text-slate-400 hover:text-brand-600 dark:hover:text-brand-400 transition-colors px-2.5 py-1.5 rounded-md hover:bg-slate-100 dark:hover:bg-slate-800">
            <PanelLeft className="w-3.5 h-3.5" />简洁
          </button>
        </div>
      </header>

      {/* 核心指标卡片 */}
      <div className="grid grid-cols-5 gap-4">
        <MetricCard icon={DollarSign} label="总资产" value={formatCurrency(totalAsset)} color="text-green-500" />
        <MetricCard
          icon={todayPnL >= 0 ? TrendingUp : TrendingDown}
          label="今日盈亏"
          value={`${todayPnL >= 0 ? '+' : ''}${formatCurrency(todayPnL)}`}
          sub={formatPercent(todayPnLPct)}
          color={todayPnL >= 0 ? 'text-green-500' : 'text-red-500'}
        />
        <MetricCard
          icon={TrendingUp}
          label="累计收益"
          value={formatPercent(totalReturn)}
          color={totalReturn >= 0 ? 'text-green-500' : 'text-red-500'}
        />
        <MetricCard
          icon={PiggyBank}
          label="持仓市值"
          value={formatCurrency(holdings)}
          color="text-brand-500"
        />
        <MetricCard
          icon={Activity}
          label="持仓/现金"
          value={`${positionsCount} 只`}
          sub={`现金 ${formatCurrency(cash)}`}
          color="text-purple-500"
        />
      </div>

      <div className="grid grid-cols-3 gap-4">
        {/* CIO状态面板 */}
        <div className="card p-4 col-span-2">
          <div className="flex items-center justify-between mb-3">
            <div className="flex items-center gap-2">
              <Shield className="w-4 h-4 text-brand-500" />
              <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">CIO 今日状态</h2>
            </div>
          </div>

          <p className="text-sm text-slate-600 dark:text-slate-400 leading-relaxed mb-4">
            {cioStatus.todayMessage}
          </p>

          {/* 团队状态 */}
          <div className="grid grid-cols-4 gap-3">
            {teamStatus.map((t) => (
              <div key={t.role} className="flex items-center gap-2 p-2 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                <t.icon className={`w-4 h-4 ${t.color}`} />
                <div>
                  <p className="text-xs font-medium text-slate-700 dark:text-slate-300">{t.name}</p>
                  <p className="text-xs text-slate-400">
                    {t.state === 'NORMAL' ? '✓ 正常' : t.state === 'WORKING' ? '⚡ 工作中' : t.state === 'IDLE' ? '⏸ 待命' : '⚠ 异常'}
                  </p>
                </div>
              </div>
            ))}
          </div>
        </div>

        {/* 风险监控面板 */}
        <div className="card p-4">
          <div className="flex items-center gap-2 mb-3">
            <Target className="w-4 h-4 text-orange-500" />
            <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">风险监控</h2>
          </div>

          <div className="space-y-3">
            <div className="flex items-center justify-between">
              <span className="text-xs text-slate-500 dark:text-slate-400">持仓集中度</span>
              <span className={`text-sm font-medium ${concentration > 30 ? 'text-yellow-600' : 'text-green-600'}`}>
                {concentration.toFixed(1)}%
              </span>
            </div>

            {topHolding && (
              <div className="flex items-center justify-between">
                <span className="text-xs text-slate-500 dark:text-slate-400">最大单票</span>
                <span className="text-sm font-medium text-slate-700 dark:text-slate-300">
                  {topHolding.stockName}
                </span>
              </div>
            )}

            <div className="flex items-center justify-between">
              <span className="text-xs text-slate-500 dark:text-slate-400">未实现盈亏</span>
              <span className={`text-sm font-medium ${getPnLColor(totalPnL)}`}>
                {totalPnL >= 0 ? '+' : ''}{formatCurrency(totalPnL)}
              </span>
            </div>

            <div className="h-px bg-slate-200 dark:bg-slate-700" />

            <div className="flex items-center gap-2 p-2 rounded bg-green-50 dark:bg-green-900/20">
              <Shield className="w-4 h-4 text-green-500" />
              <span className="text-xs text-green-700 dark:text-green-400">风控规则正常</span>
            </div>
          </div>
        </div>
      </div>

      {/* 持仓分布 + AI 今日决策 */}
      <div className="grid grid-cols-2 gap-4">
        {/* 持仓分布表格 */}
        {positions.length > 0 && (
          <div className="card p-4">
            <div className="flex items-center justify-between mb-3">
              <div className="flex items-center gap-2">
                <PieChart className="w-4 h-4 text-brand-500" />
                <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">持仓分布</h2>
              </div>
              <span className="text-xs text-slate-400">共 {positions.length} 只</span>
            </div>

            <div className="overflow-hidden rounded-lg border border-slate-200 dark:border-slate-700">
              <table className="w-full text-sm">
                <thead>
                  <tr className="bg-slate-50 dark:bg-slate-900/50">
                    <th className="px-3 py-2 text-left text-xs font-medium text-slate-500 dark:text-slate-400">股票名称（代码）</th>
                    <th className="px-3 py-2 text-right text-xs font-medium text-slate-500 dark:text-slate-400">持仓量</th>
                    <th className="px-3 py-2 text-right text-xs font-medium text-slate-500 dark:text-slate-400">盈亏</th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
                  {pagePositions.map((p) => (
                    <tr key={p.code} className="hover:bg-slate-50 dark:hover:bg-slate-900/30">
                      <td className="px-3 py-2">
                        <div className="flex items-center gap-1.5">
                          <span className="text-sm font-medium text-slate-700 dark:text-slate-200">{p.stockName}</span>
                          <span className="text-xs text-slate-400">{p.code}</span>
                        </div>
                      </td>
                      <td className="px-3 py-2 text-right text-sm text-slate-600 dark:text-slate-300">{p.quantity}</td>
                      <td className="px-3 py-2 text-right">
                        <span className={`text-sm font-medium ${getPnLColor(p.unrealizedPnL)}`}>
                          {p.unrealizedPnL >= 0 ? '+' : ''}{formatCurrency(p.unrealizedPnL)}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>

            {/* 分页控件 */}
            {holdingsTotalPages > 1 && (
              <div className="flex items-center justify-between mt-3">
                <span className="text-xs text-slate-400">
                  第 {safeHoldingsPage + 1} / {holdingsTotalPages} 页
                </span>
                <div className="flex items-center gap-2">
                  <button
                    onClick={() => onHoldingsPageChange(Math.max(0, safeHoldingsPage - 1))}
                    disabled={safeHoldingsPage === 0}
                    className="px-2 py-1 text-xs rounded border border-slate-200 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-40 disabled:cursor-not-allowed"
                  >
                    上一页
                  </button>
                  <button
                    onClick={() => onHoldingsPageChange(Math.min(holdingsTotalPages - 1, safeHoldingsPage + 1))}
                    disabled={safeHoldingsPage >= holdingsTotalPages - 1}
                    className="px-2 py-1 text-xs rounded border border-slate-200 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800 disabled:opacity-40 disabled:cursor-not-allowed"
                  >
                    下一页
                  </button>
                </div>
              </div>
            )}
          </div>
        )}

        {/* AI 今日决策 */}
        <div className="card p-4">
          <div className="flex items-center justify-between mb-3">
            <div className="flex items-center gap-2">
              <Brain className="w-4 h-4 text-purple-500" />
              <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">AI 今日决策</h2>
            </div>
            <span className="text-xs text-slate-400">{decisions.length} 条</span>
          </div>

          {decisionsLoading ? (
            <div className="flex items-center justify-center py-12">
              <Loader2 className="w-5 h-5 animate-spin text-slate-400" />
            </div>
          ) : decisions.length > 0 ? (
            <div className="space-y-2 max-h-64 overflow-auto pr-1">
              {decisions.slice(0, 5).map((d) => (
                <div key={d.decisionID} className="p-2.5 rounded-lg bg-slate-50 dark:bg-slate-900/50">
                  <div className="flex items-center justify-between mb-1">
                    <div className="flex items-center gap-2">
                      <span className={`px-1.5 py-0.5 rounded text-[10px] font-medium ${
                        d.decision === 'REBALANCE' ? 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-400' :
                        d.decision === 'REDUCE' || d.decision === 'REDUCE_RISK' ? 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-400' :
                        d.decision === 'PAUSE_TRADING' ? 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/40 dark:text-yellow-400' :
                        d.decision === 'NO_ACTION' || d.decision === 'HOLD' ? 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-400' :
                        d.decision === 'BUILD' ? 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-400' :
                        d.decision === 'INCREASE' || d.decision === 'INCREASE_RISK' || d.decision === 'BUY' ? 'bg-cyan-100 text-cyan-700 dark:bg-cyan-900/40 dark:text-cyan-400' :
                        'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-400'
                      }`}>
                        {d.decision === 'REBALANCE' ? '调仓' :
                         d.decision === 'REDUCE' || d.decision === 'REDUCE_RISK' ? '减仓' :
                         d.decision === 'PAUSE_TRADING' ? '暂停' :
                         d.decision === 'NO_ACTION' ? '观望' :
                         d.decision === 'HOLD' ? '持有' :
                         d.decision === 'BUILD' ? '建仓' :
                         d.decision === 'INCREASE' || d.decision === 'INCREASE_RISK' ? '加仓' :
                         d.decision === 'BUY' ? '买入' :
                         d.decision === 'SELL' ? '卖出' : d.decision}
                      </span>
                      <span className="text-xs text-slate-400">{d.timestamp}</span>
                    </div>
                    {d.riskApproval && (
                      <span className={`text-[10px] ${d.riskApproval === 'APPROVED' ? 'text-green-500' : d.riskApproval === 'REJECTED' ? 'text-red-500' : 'text-yellow-500'}`}>
                        {d.riskApproval === 'APPROVED' ? '✓ 通过' : d.riskApproval === 'REJECTED' ? '✗ 否决' : '⚠ 审核'}
                      </span>
                    )}
                  </div>
                  <p className="text-xs text-slate-600 dark:text-slate-400 leading-relaxed">{d.reason}</p>
                  {d.orders && d.orders.length > 0 && (
                    <div className="flex flex-wrap gap-1 mt-1.5">
                      {d.orders.slice(0, 3).map((o, i) => (
                        <span key={i} className={`text-[10px] px-1.5 py-0.5 rounded ${o.side === 'BUY' ? 'bg-green-50 text-green-600 dark:bg-green-900/30 dark:text-green-400' : 'bg-red-50 text-red-600 dark:bg-red-900/30 dark:text-red-400'}`}>
                          {o.side === 'BUY' ? '买' : '卖'} {o.code}
                        </span>
                      ))}
                      {d.orders.length > 3 && (
                        <span className="text-[10px] text-slate-400">+{d.orders.length - 3}</span>
                      )}
                    </div>
                  )}
                </div>
              ))}
            </div>
          ) : (
            <div className="flex flex-col items-center justify-center py-10 text-center">
              <Brain className="w-8 h-8 text-slate-300 dark:text-slate-600 mb-2" />
              <p className="text-xs text-slate-500 dark:text-slate-400">今日暂无决策记录</p>
              <p className="text-[10px] text-slate-400 mt-1">运行每日检查后将生成决策</p>
            </div>
          )}
        </div>
      </div>

      <footer className="flex items-center justify-between px-4 py-3 bg-white dark:bg-slate-800 rounded-xl border border-slate-200 dark:border-slate-700">
        <div className="flex items-center gap-6 text-xs text-slate-500 dark:text-slate-400">
          <span className="flex items-center gap-1.5">
            <Activity className="w-3.5 h-3.5" />AI 团队在线
          </span>
          <span className="flex items-center gap-1.5">
            <AlertTriangle className={`w-3.5 h-3.5 ${isStopped ? 'text-yellow-500' : 'text-green-500'}`} />
            {isStopped ? '交易停止' : '0 风险警报'}
          </span>
        </div>
        <div className="text-xs text-slate-400 flex items-center gap-2">
          {refreshing && <Loader2 className="w-3 h-3 animate-spin" />}
          {isStopped ? '系统已停止' : '你的 AI 投资团队正在工作'}
        </div>
      </footer>
    </div>
  )
}

function MetricCard({
  icon: Icon,
  label,
  value,
  sub,
  color,
}: {
  icon: typeof DollarSign
  label: string
  value: string
  sub?: string
  color: string
}) {
  return (
    <div className="card p-3">
      <div className="flex items-center gap-2 mb-1">
        <Icon className={`w-4 h-4 ${color}`} />
        <span className="text-xs text-slate-500 dark:text-slate-400">{label}</span>
      </div>
      <div className="text-lg font-bold text-slate-800 dark:text-slate-100 truncate">{value}</div>
      {sub && <div className="text-xs text-slate-400 mt-0.5">{sub}</div>}
    </div>
  )
}
