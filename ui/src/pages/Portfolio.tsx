import { useState, useEffect, useMemo, useCallback, useRef } from 'react'
import { Briefcase, TrendingUp, TrendingDown, DollarSign, Wallet, RefreshCw, Radio, Search, ChevronLeft, ChevronRight, X, ArrowRightLeft, ShoppingCart, Activity, LineChart, BarChart3, Calendar } from 'lucide-react'
import {
  GetPortfolioState,
  GetTradeHistory,
  GetOrders,
  TriggerRebalance,
  RefreshPrices as RefreshPricesAPI,
  ExecuteManualTrade,
  GetProfitHistory,
  GetLatestDailyStat,
  RecordDailySnapshot,
  GetPortfolioNavBenchmark,
  IsReady,
} from '../../wailsjs/go/main/App'
import { formatCurrency, formatPercent } from '../utils/formatters'
import { useToastStore } from '../store/toastStore'
import {
  LineChart as RechartsLineChart,
  Line,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  AreaChart,
  Area,
  BarChart,
  Bar,
} from 'recharts'

interface Position {
  code: string
  stockName: string
  quantity: number
  currentPrice: number
  avgCost: number
  marketValue: number
  unrealizedPnL: number
  unrealizedReturn: number
  weight: number
  lastUpdate: string
}

interface TradeRecord {
  tradeID: string
  side: string
  instrumentID: string
  quantity: number
  price: number
  grossAmount: number
  commission: number
  fees: number
  netAmount: number
  realizedPnL: number
  tradeDate: string
}

interface OrderRecord {
  orderId: string
  side: string
  instrumentId: string
  stockName: string
  quantity: number
  price: number
  amount: number
  status: string // pending / filled / cancelled
  submittedAt: string | null
  filledAt: string | null
  createdAt: string
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
  recentTrades: TradeRecord[]
  ledger?: LedgerInfo
  lastUpdated: string
}

/** 财务流水账四账对账信息（期初账/临时账/明细账/总账） */
interface LedgerInfo {
  opening_capital: number
  temp: { pending_orders: number; reserved_buy: number }
  detail: { trade_count: number; buy_count: number; buy_net: number; sell_count: number; sell_net: number }
  general: { date: string; total_assets: number; total_pnl: number; daily_pnl: number }
  cash: { derived: number; on_record: number }
  total_assets: number
  reconciled: boolean
  issues: string[]
}

/** 表格分页组件（10行/页） */
function TablePagination({ total, currentPage, totalPages, onPageChange }: {
  total: number
  currentPage: number
  totalPages: number
  onPageChange: (page: number) => void
}) {
  if (total <= 0 || totalPages <= 1) return null
  return (
    <div className="flex items-center justify-between px-5 py-3 border-t border-slate-200 dark:border-slate-700">
      <span className="text-xs text-slate-500 dark:text-slate-400">
        共 {total} 条 · 第 {currentPage}/{totalPages} 页
      </span>
      <div className="flex items-center gap-1">
        <button
          onClick={() => onPageChange(Math.max(1, currentPage - 1))}
          disabled={currentPage <= 1}
          className="p-1.5 rounded-lg border border-slate-200 dark:border-slate-700 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-slate-50 dark:hover:bg-slate-800"
        >
          <ChevronLeft className="w-4 h-4" />
        </button>
        {Array.from({ length: totalPages }, (_, i) => i + 1)
          .filter((p) => p === 1 || p === totalPages || Math.abs(p - currentPage) <= 1)
          .map((p, idx, arr) => (
            <span key={p} className="flex items-center">
              {idx > 0 && p - arr[idx - 1] > 1 && (
                <span className="px-1 text-slate-400">...</span>
              )}
              <button
                onClick={() => onPageChange(p)}
                className={`w-8 h-8 rounded-lg text-sm ${
                  currentPage === p
                    ? 'bg-indigo-500 text-white'
                    : 'border border-slate-200 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800'
                }`}
              >
                {p}
              </button>
            </span>
          ))}
        <button
          onClick={() => onPageChange(Math.min(totalPages, currentPage + 1))}
          disabled={currentPage >= totalPages}
          className="p-1.5 rounded-lg border border-slate-200 dark:border-slate-700 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-slate-50 dark:hover:bg-slate-800"
        >
          <ChevronRight className="w-4 h-4" />
        </button>
      </div>
    </div>
  )
}

export default function Portfolio() {
  const [portfolio, setPortfolio] = useState<PortfolioState | null>(null)
  const [trades, setTrades] = useState<TradeRecord[]>([])
  const [orders, setOrders] = useState<OrderRecord[]>([])
  const [ordersFilter, setOrdersFilter] = useState<'all' | 'pending'>('pending')
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [changeFilter, setChangeFilter] = useState<'all' | 'up' | 'down'>('all')
  const [currentPage, setCurrentPage] = useState(1)
  const pageSize = 10
  const [tradesPage, setTradesPage] = useState(1)
  const [profitPage, setProfitPage] = useState(1)
  const [activeTab, setActiveTab] = useState<'positions' | 'trades' | 'trading' | 'profit'>('positions')

  // 盈亏分析数据
  const [profitHistory, setProfitHistory] = useState<any[]>([])
  const [navBenchmarkData, setNavBenchmarkData] = useState<any>(null)
  const [latestDailyStat, setLatestDailyStat] = useState<any>(null)

  // 数据获取失败弹窗去重标记（成功后重置）
  const failNotifiedRef = useRef(false)
  const [profitPeriod, setProfitPeriod] = useState<'1' | '7' | '30' | '90' | '365'>('30')
  const [profitLoading, setProfitLoading] = useState(false)

  // 手动交易表单
  const [tradeForm, setTradeForm] = useState({
    symbol: '',
    side: 'BUY' as 'BUY' | 'SELL',
    quantity: 100,
    price: 0,
    reason: '手动交易',
  })
  const [tradeMessage, setTradeMessage] = useState<{ type: 'success' | 'error'; text: string } | null>(null)

  // 等待后端就绪
  const waitForReady = useCallback(async (maxRetries: number = 30): Promise<{ready: boolean, portfolioReady: boolean}> => {
    for (let i = 0; i < maxRetries; i++) {
      try {
        const status = await IsReady()
        if (status?.ready) {
          return {ready: true, portfolioReady: status?.portfolio_ready || false}
        }
      } catch {
        // API 调用失败，继续等待
      }
      await new Promise(resolve => setTimeout(resolve, 1000))
    }
    return {ready: false, portfolioReady: false}
  }, [])

  const loadData = useCallback(async () => {
    setLoading(true)
    setRefreshing(true)
    try {
      // 先等待后端就绪
      const {ready, portfolioReady} = await waitForReady(30)
      if (!ready) {
        setTradeMessage({
          type: 'error',
          text: '系统初始化超时，请重启应用或查看日志',
        })
        return
      }

      const [state, tradeHistory] = await Promise.all([
        GetPortfolioState(),
        GetTradeHistory(50),
      ])
      setPortfolio(state)
      setTrades(tradeHistory?.trades || [])
      failNotifiedRef.current = false
      
      if (!portfolioReady) {
        setTradeMessage({
          type: 'error',
          text: '组合引擎未初始化，请查看日志',
        })
      }
    } catch (err) {
      console.error('Failed to load portfolio:', err)
      setTradeMessage({
        type: 'error',
        text: '加载数据失败，请重试',
      })
      // 数据获取失败：弹窗警示（已通过定时轮询自动重试）
      if (!failNotifiedRef.current) {
        failNotifiedRef.current = true
        useToastStore.getState().error('数据获取失败，将自动重试', err instanceof Error ? err.message : String(err))
      }
    } finally {
      setLoading(false)
      setRefreshing(false)
    }
  }, [waitForReady])

  const loadProfitHistory = useCallback(async (period: string) => {
    setProfitLoading(true)
    try {
      const data = await GetProfitHistory(parseInt(period))
      if (data?.points) {
        setProfitHistory(data.points)
      } else {
        setProfitHistory([])
      }
    } catch {
      setProfitHistory([])
    } finally {
      setProfitLoading(false)
    }
  }, [])

  // 订单队列：读取 orders 临时表（待确认/已成交/已取消）
  const loadOrders = useCallback(async (includeFilled: boolean) => {
    try {
      const data = await GetOrders(includeFilled)
      setOrders(data || [])
    } catch {
      setOrders([])
    }
  }, [])

  const loadLatestDailyStat = useCallback(async () => {
    try {
      const data = await GetLatestDailyStat()
      setLatestDailyStat(data)
    } catch {
      setLatestDailyStat(null)
    }
  }, [])

  const loadNavBenchmark = useCallback(async () => {
    try {
      const data = await GetPortfolioNavBenchmark(365)
      setNavBenchmarkData(data)
    } catch {
      setNavBenchmarkData(null)
    }
  }, [])

  useEffect(() => {
    loadData()
    loadProfitHistory(profitPeriod)
    loadLatestDailyStat()
    loadNavBenchmark()
  }, [loadData, profitPeriod, loadProfitHistory, loadLatestDailyStat, loadNavBenchmark])

  // 实时刷新：每30秒从 sqlite 权威数据重建组合状态（含持仓/成交/盈亏），无需手动刷新
  useEffect(() => {
    const interval = setInterval(async () => {
      try {
        await RefreshPricesAPI()
      } catch {
        // 行情刷新失败不阻断数据轮询
      }
      loadData()
    }, 30 * 1000)
    return () => clearInterval(interval)
  }, [loadData])

  const handleRebalance = async () => {
    setRefreshing(true)
    try {
      const result = await TriggerRebalance()
      if (result?.portfolio) {
        setPortfolio(result.portfolio)
      }
      if (result?.trades) {
        setTrades(result.trades)
      }
      setTradeMessage({ type: 'success', text: '再平衡完成！' })
      setTimeout(() => setTradeMessage(null), 3000)
    } catch (err: any) {
      setTradeMessage({ type: 'error', text: err?.toString() || '再平衡失败' })
    } finally {
      setRefreshing(false)
      setTimeout(() => {
        setTradeMessage(null)
      }, 3000)
    }
  }

  const handleManualTrade = async () => {
    if (!tradeForm.symbol || tradeForm.price <= 0 || tradeForm.quantity <= 0) {
      setTradeMessage({ type: 'error', text: '请填写完整的交易信息' })
      setTimeout(() => setTradeMessage(null), 3000)
      return
    }
    setRefreshing(true)
    try {
      await ExecuteManualTrade(
        tradeForm.symbol,
        tradeForm.side,
        tradeForm.quantity,
        tradeForm.price,
        tradeForm.reason
      )
      setTradeMessage({ type: 'success', text: `${tradeForm.side === 'BUY' ? '买入' : '卖出'}成功！` })
      setTradeForm({ ...tradeForm, symbol: '', quantity: 100, price: 0 })
      loadData()
    } catch (err: any) {
      setTradeMessage({ type: 'error', text: err?.toString() || '交易失败' })
    } finally {
      setRefreshing(false)
      setTimeout(() => setTradeMessage(null), 3000)
    }
  }

  const handleTabChange = (tab: 'positions' | 'trades' | 'trading' | 'profit') => {
    setActiveTab(tab)
    // 切换页签时实时刷新对应数据，确保从 sqlite 权威数据同步最新状态
    loadData()
    if (tab === 'trading') {
      loadOrders(ordersFilter === 'all')
    }
    if (tab === 'profit') {
      loadProfitHistory(profitPeriod)
      loadLatestDailyStat()
      loadNavBenchmark()
    }
  }

  const handleRecordSnapshot = async () => {
    setRefreshing(true)
    try {
      const result = await RecordDailySnapshot()
      if (result?.status === 'ok') {
        setTradeMessage({ type: 'success', text: '每日持仓快照已记录' })
        loadProfitHistory(profitPeriod)
        loadLatestDailyStat()
      }
    } catch (err: any) {
      setTradeMessage({ type: 'error', text: err?.toString() || '快照记录失败' })
    } finally {
      setRefreshing(false)
      setTimeout(() => setTradeMessage(null), 3000)
    }
  }

  const positions = portfolio?.positions || []

  const filteredPositions = useMemo(() => {
    let list = positions
    if (searchQuery.trim()) {
      const q = searchQuery.trim().toLowerCase()
      list = list.filter(
        (p) => p.stockName?.toLowerCase().includes(q) || p.code?.toLowerCase().includes(q)
      )
    }
    if (changeFilter === 'up') {
      list = list.filter((p) => p.unrealizedReturn >= 0)
    } else if (changeFilter === 'down') {
      list = list.filter((p) => p.unrealizedReturn < 0)
    }
    return list
  }, [positions, searchQuery, changeFilter])

  const totalPages = Math.max(1, Math.ceil(filteredPositions.length / pageSize))
  const safePage = Math.min(currentPage, totalPages)
  const pagedPositions = filteredPositions.slice((safePage - 1) * pageSize, safePage * pageSize)

  // 交易记录分页
  const tradesTotalPages = Math.max(1, Math.ceil(trades.length / pageSize))
  const safeTradesPage = Math.min(tradesPage, tradesTotalPages)
  const pagedTrades = trades.slice((safeTradesPage - 1) * pageSize, safeTradesPage * pageSize)

  // 每日盈亏明细分页（时间倒序展示）
  const reversedProfit = [...profitHistory].reverse()
  const profitTotalPages = Math.max(1, Math.ceil(reversedProfit.length / pageSize))
  const safeProfitPage = Math.min(profitPage, profitTotalPages)
  const pagedProfit = reversedProfit.slice((safeProfitPage - 1) * pageSize, safeProfitPage * pageSize)

  useEffect(() => {
    setCurrentPage(1)
  }, [searchQuery, changeFilter])

  // 数据变更后重置分页
  useEffect(() => {
    setTradesPage(1)
  }, [trades])

  useEffect(() => {
    setProfitPage(1)
  }, [profitHistory])

  if (loading && !portfolio) {
    return (
      <div className="flex items-center justify-center h-64">
        <div className="animate-spin rounded-full h-12 w-12 border-b-2 border-indigo-500"></div>
      </div>
    )
  }

  const totalAssets = portfolio?.totalAssets || 0
  const totalPnL = portfolio?.totalPnL || 0
  const totalReturn = portfolio?.totalReturn || 0
  const dailyPnL = portfolio?.dailyPnL || 0
  const cash = portfolio?.cash || 0
  const positionsCount = portfolio?.positionsCount || 0

  return (
    <div className="space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-brand-500 to-cyan-400 flex items-center justify-center shadow-sm">
            <Briefcase className="w-5 h-5 text-white" />
          </div>
          <div>
            <h1 className="text-2xl font-bold text-slate-800 dark:text-slate-100">组合交易中心</h1>
            <p className="text-sm text-slate-500 dark:text-slate-400 mt-0.5">
              AI量化基金 · 实时持仓监控 · 共 {positionsCount} 只持仓
            </p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          <span className="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400">
            <Radio className="w-3.5 h-3.5" />
            实时持仓监控
          </span>
        </div>
      </div>

      {/* Alert Message */}
      {tradeMessage && (
        <div
          className={`p-3 rounded-lg text-sm ${
            tradeMessage.type === 'success'
              ? 'bg-green-50 text-green-700 border border-green-200 dark:bg-green-900/20 dark:text-green-400 dark:border-green-800'
              : 'bg-red-50 text-red-700 border border-red-200 dark:bg-red-900/20 dark:text-red-400 dark:border-red-800'
          }`}
        >
          {tradeMessage.text}
        </div>
      )}

      {/* Summary Cards */}
      <div className="grid grid-cols-1 md:grid-cols-4 gap-4">
        <div className="card p-5">
          <div className="flex items-center gap-3 mb-2">
            <div className="w-10 h-10 rounded-lg bg-blue-50 dark:bg-blue-900/30 flex items-center justify-center">
              <DollarSign className="w-5 h-5 text-blue-500" />
            </div>
          </div>
          <div className="text-xs text-slate-500 dark:text-slate-400">总资产</div>
          <div className="text-xl font-bold text-slate-800 dark:text-slate-100">{formatCurrency(totalAssets)}</div>
          <div className={`text-xs mt-1 ${totalReturn >= 0 ? 'text-green-500' : 'text-red-500'}`}>
            累计收益: {totalReturn >= 0 ? '+' : ''}{totalReturn.toFixed(2)}%
          </div>
        </div>

        <div className="card p-5">
          <div className="flex items-center gap-3 mb-2">
            <div className={`w-10 h-10 rounded-lg flex items-center justify-center ${dailyPnL >= 0 ? 'bg-green-50 dark:bg-green-900/30' : 'bg-red-50 dark:bg-red-900/30'}`}>
              {dailyPnL >= 0 ? (
                <TrendingUp className="w-5 h-5 text-green-500" />
              ) : (
                <TrendingDown className="w-5 h-5 text-red-500" />
              )}
            </div>
          </div>
          <div className="text-xs text-slate-500 dark:text-slate-400">今日盈亏</div>
          <div className={`text-xl font-bold ${dailyPnL >= 0 ? 'text-green-500' : 'text-red-500'}`}>
            {dailyPnL >= 0 ? '+' : ''}{formatCurrency(dailyPnL)}
          </div>
        </div>

        <div className="card p-5">
          <div className="flex items-center gap-3 mb-2">
            <div className={`w-10 h-10 rounded-lg flex items-center justify-center ${totalPnL >= 0 ? 'bg-green-50 dark:bg-green-900/30' : 'bg-red-50 dark:bg-red-900/30'}`}>
              <Activity className={`w-5 h-5 ${totalPnL >= 0 ? 'text-green-500' : 'text-red-500'}`} />
            </div>
          </div>
          <div className="text-xs text-slate-500 dark:text-slate-400">累计盈亏</div>
          <div className={`text-xl font-bold ${totalPnL >= 0 ? 'text-green-500' : 'text-red-500'}`}>
            {totalPnL >= 0 ? '+' : ''}{formatCurrency(totalPnL)}
          </div>
        </div>

        <div className="card p-5">
          <div className="flex items-center gap-3 mb-2">
            <div className="w-10 h-10 rounded-lg bg-amber-50 dark:bg-amber-900/30 flex items-center justify-center">
              <Wallet className="w-5 h-5 text-amber-500" />
            </div>
          </div>
          <div className="text-xs text-slate-500 dark:text-slate-400">可用资金</div>
          <div className="text-xl font-bold text-slate-800 dark:text-slate-100">{formatCurrency(cash)}</div>
        </div>
      </div>

      {/* Tab Navigation */}
      <div className="flex items-center gap-1 bg-slate-100 dark:bg-slate-800 rounded-lg p-1">
        {([
          { key: 'positions', label: '持仓明细', icon: Briefcase },
          { key: 'trades', label: '交易记录', icon: ShoppingCart },
          { key: 'trading', label: '交易操作', icon: ArrowRightLeft },
          { key: 'profit', label: '盈亏分析', icon: LineChart },
        ] as const).map((tab) => (
          <button
            key={tab.key}
            onClick={() => handleTabChange(tab.key)}
            className={`flex-1 flex items-center justify-center gap-2 px-4 py-2 rounded-md text-sm font-medium transition-all ${
              activeTab === tab.key
                ? 'bg-white dark:bg-slate-700 shadow text-slate-800 dark:text-slate-100'
                : 'text-slate-500 dark:text-slate-400 hover:text-slate-700'
            }`}
          >
            <tab.icon className="w-4 h-4" />
            {tab.label}
          </button>
        ))}
      </div>

      {/* Positions Tab */}
      {activeTab === 'positions' && (
        <div className="card overflow-hidden">
          <div className="p-5 border-b border-slate-200 dark:border-slate-700">
            <div className="flex items-center justify-between mb-4">
              <h3 className="font-medium text-slate-800 dark:text-slate-100">持仓明细 · {filteredPositions.length} 只</h3>
              <button
                onClick={handleRebalance}
                className="btn-primary text-xs py-1.5 px-3"
                disabled={refreshing}
              >
                <ArrowRightLeft className="w-3.5 h-3.5 mr-1" />
                AI再平衡
              </button>
            </div>

            <div className="flex items-center gap-2">
              <div className="flex-1 relative">
                <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-400" />
                <input
                  type="text"
                  value={searchQuery}
                  onChange={(e) => setSearchQuery(e.target.value)}
                  placeholder="搜索股票名称或代码..."
                  className="w-full pl-9 pr-8 py-2 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                />
                {searchQuery && (
                  <button
                    onClick={() => setSearchQuery('')}
                    className="absolute right-2 top-1/2 -translate-y-1/2 text-slate-400 hover:text-slate-600"
                  >
                    <X className="w-4 h-4" />
                  </button>
                )}
              </div>
              <div className="flex items-center gap-1 bg-slate-100 dark:bg-slate-700 rounded-lg p-1">
                {([
                  { key: 'all', label: '全部' },
                  { key: 'up', label: '盈利' },
                  { key: 'down', label: '亏损' },
                ] as const).map((opt) => (
                  <button
                    key={opt.key}
                    onClick={() => setChangeFilter(opt.key)}
                    className={`px-3 py-1 rounded-md text-xs transition-all ${
                      changeFilter === opt.key
                        ? 'bg-white dark:bg-slate-900 shadow text-slate-700 dark:text-slate-200'
                        : 'text-slate-500 dark:text-slate-400'
                    }`}
                  >
                    {opt.label}
                  </button>
                ))}
              </div>
            </div>
          </div>

          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="text-left text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-700">
                  <th className="px-5 py-3 font-medium">代码</th>
                  <th className="px-5 py-3 font-medium">名称</th>
                  <th className="px-5 py-3 font-medium">持仓量</th>
                  <th className="px-5 py-3 font-medium">成本价</th>
                  <th className="px-5 py-3 font-medium">现价</th>
                  <th className="px-5 py-3 font-medium">市值</th>
                  <th className="px-5 py-3 font-medium">盈亏</th>
                  <th className="px-5 py-3 font-medium">收益率</th>
                  <th className="px-5 py-3 font-medium">权重</th>
                </tr>
              </thead>
              <tbody>
                {pagedPositions.length > 0 ? (
                  pagedPositions.map((p) => {
                    const isUp = p.unrealizedPnL >= 0
                    return (
                      <tr
                        key={p.code}
                        className="border-b border-slate-100 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800/50 transition-colors"
                      >
                        <td className="px-5 py-4">
                          <span className="font-mono text-sm font-medium text-slate-800 dark:text-slate-100">
                            {p.code}
                          </span>
                        </td>
                        <td className="px-5 py-4 text-slate-700 dark:text-slate-300">{p.stockName}</td>
                        <td className="px-5 py-4 text-slate-800 dark:text-slate-100">{p.quantity.toLocaleString()}</td>
                        <td className="px-5 py-4 text-slate-600 dark:text-slate-400">¥{p.avgCost.toFixed(2)}</td>
                        <td className={`px-5 py-4 font-medium ${isUp ? 'text-red-500' : 'text-green-500'}`}>
                          ¥{p.currentPrice.toFixed(2)}
                        </td>
                        <td className="px-5 py-4 font-semibold text-slate-800 dark:text-slate-100">
                          {formatCurrency(p.marketValue)}
                        </td>
                        <td className={`px-5 py-4 font-medium ${isUp ? 'text-green-500' : 'text-red-500'}`}>
                          {isUp ? '+' : ''}{formatCurrency(p.unrealizedPnL)}
                        </td>
                        <td className="px-5 py-4">
                          <span className={`inline-flex items-center gap-1 font-medium ${isUp ? 'text-green-500' : 'text-red-500'}`}>
                            {isUp ? <TrendingUp className="w-3 h-3" /> : <TrendingDown className="w-3 h-3" />}
                            {isUp ? '+' : ''}{p.unrealizedReturn.toFixed(2)}%
                          </span>
                        </td>
                        <td className="px-5 py-4 text-slate-600 dark:text-slate-400">
                          {p.weight.toFixed(2)}%
                        </td>
                      </tr>
                    )
                  })
                ) : (
                  <tr>
                    <td colSpan={9} className="text-center py-8 text-slate-400 dark:text-slate-500">
                      暂无持仓数据，请先通过"交易操作"买入股票或"AI再平衡"
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>

          <TablePagination
            total={filteredPositions.length}
            currentPage={safePage}
            totalPages={totalPages}
            onPageChange={setCurrentPage}
          />
        </div>
      )}

      {/* Trades Tab */}
      {activeTab === 'trades' && (
        <div className="card overflow-hidden">
          <div className="p-5 border-b border-slate-200 dark:border-slate-700">
            <h3 className="font-medium text-slate-800 dark:text-slate-100">交易记录 · 共 {trades.length} 条</h3>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full">
              <thead>
                <tr className="text-left text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-700">
                  <th className="px-5 py-3 font-medium">时间</th>
                  <th className="px-5 py-3 font-medium">方向</th>
                  <th className="px-5 py-3 font-medium">代码</th>
                  <th className="px-5 py-3 font-medium">数量</th>
                  <th className="px-5 py-3 font-medium">价格</th>
                  <th className="px-5 py-3 font-medium">成交金额</th>
                  <th className="px-5 py-3 font-medium">佣金</th>
                  <th className="px-5 py-3 font-medium">税费</th>
                  <th className="px-5 py-3 font-medium">净额</th>
                  <th className="px-5 py-3 font-medium">已实现盈亏</th>
                </tr>
              </thead>
              <tbody>
                {trades.length > 0 ? (
                  pagedTrades.map((t) => {
                    const isBuy = t.side === 'BUY'
                    return (
                      <tr
                        key={t.tradeID}
                        className="border-b border-slate-100 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800/50 transition-colors"
                      >
                        <td className="px-5 py-3 text-sm text-slate-600 dark:text-slate-400">
                          {new Date(t.tradeDate).toLocaleString('zh-CN')}
                        </td>
                        <td className="px-5 py-3">
                          <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${
                            isBuy
                              ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
                              : 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
                          }`}>
                            {isBuy ? '买入' : '卖出'}
                          </span>
                        </td>
                        <td className="px-5 py-3 font-mono text-sm">{t.instrumentID}</td>
                        <td className="px-5 py-3">{t.quantity.toLocaleString()}</td>
                        <td className="px-5 py-3">¥{t.price.toFixed(2)}</td>
                        <td className="px-5 py-3">{formatCurrency(t.grossAmount)}</td>
                        <td className="px-5 py-3 text-slate-500">{formatCurrency(t.commission)}</td>
                        <td className="px-5 py-3 text-slate-500">{formatCurrency(t.fees)}</td>
                        <td className="px-5 py-3 font-semibold">{formatCurrency(t.netAmount)}</td>
                        <td className={`px-5 py-3 font-medium ${t.realizedPnL >= 0 ? 'text-green-500' : 'text-red-500'}`}>
                          {t.realizedPnL >= 0 ? '+' : ''}{formatCurrency(t.realizedPnL)}
                        </td>
                      </tr>
                    )
                  })
                ) : (
                  <tr>
                    <td colSpan={10} className="text-center py-8 text-slate-400 dark:text-slate-500">
                      暂无交易记录
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
          <TablePagination
            total={trades.length}
            currentPage={safeTradesPage}
            totalPages={tradesTotalPages}
            onPageChange={setTradesPage}
          />
        </div>
      )}

      {/* Trading Tab */}
      {activeTab === 'trading' && (
        <>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          {/* Manual Trade */}
          <div className="card p-6">
            <h3 className="font-medium text-slate-800 dark:text-slate-100 mb-4 flex items-center gap-2">
              <ShoppingCart className="w-5 h-5 text-indigo-500" />
              手动交易
            </h3>
            <div className="space-y-4">
              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">股票代码 (支持 sh600519 或 600519)</label>
                <input
                  type="text"
                  value={tradeForm.symbol}
                  onChange={(e) => setTradeForm({ ...tradeForm, symbol: e.target.value })}
                  placeholder="sh600519 或 600519"
                  className="w-full px-3 py-2 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500 font-mono"
                />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">方向</label>
                  <div className="flex gap-2">
                    <button
                      onClick={() => setTradeForm({ ...tradeForm, side: 'BUY' })}
                      className={`flex-1 py-2 rounded-lg text-sm font-medium ${
                        tradeForm.side === 'BUY'
                          ? 'bg-red-500 text-white'
                          : 'bg-slate-100 dark:bg-slate-800 text-slate-600'
                      }`}
                    >
                      买入
                    </button>
                    <button
                      onClick={() => setTradeForm({ ...tradeForm, side: 'SELL' })}
                      className={`flex-1 py-2 rounded-lg text-sm font-medium ${
                        tradeForm.side === 'SELL'
                          ? 'bg-green-500 text-white'
                          : 'bg-slate-100 dark:bg-slate-800 text-slate-600'
                      }`}
                    >
                      卖出
                    </button>
                  </div>
                </div>
                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">数量 (股)</label>
                  <input
                    type="number"
                    value={tradeForm.quantity}
                    onChange={(e) => setTradeForm({ ...tradeForm, quantity: parseInt(e.target.value) || 0 })}
                    step={100}
                    className="w-full px-3 py-2 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                  />
                </div>
              </div>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">价格</label>
                  <input
                    type="number"
                    step={0.01}
                    value={tradeForm.price || ''}
                    onChange={(e) => setTradeForm({ ...tradeForm, price: parseFloat(e.target.value) || 0 })}
                    placeholder="0.00"
                    className="w-full px-3 py-2 bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-indigo-500"
                  />
                </div>
                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">交易金额</label>
                  <div className="px-3 py-2 bg-slate-50 dark:bg-slate-800 rounded-lg text-sm font-medium text-slate-800 dark:text-slate-100">
                    {formatCurrency((tradeForm.price || 0) * (tradeForm.quantity || 0))}
                  </div>
                </div>
              </div>
              <button
                onClick={handleManualTrade}
                disabled={refreshing || !tradeForm.symbol || tradeForm.price <= 0}
                className={`w-full py-2.5 rounded-lg text-sm font-medium ${
                  tradeForm.side === 'BUY'
                    ? 'bg-red-500 hover:bg-red-600 text-white'
                    : 'bg-green-500 hover:bg-green-600 text-white'
                } disabled:opacity-50 disabled:cursor-not-allowed transition-colors`}
              >
                {tradeForm.side === 'BUY' ? '确认买入' : '确认卖出'}
              </button>
              <p className="text-xs text-slate-500 dark:text-slate-400">
                注意：A股买入必须为100的整数倍，价格为0时将自动获取实时行情
              </p>
            </div>
          </div>

          {/* Trading Controls */}
          <div className="card p-6">
            <h3 className="font-medium text-slate-800 dark:text-slate-100 mb-4 flex items-center gap-2">
              <Activity className="w-5 h-5 text-purple-500" />
              交易控制
            </h3>
            <div className="space-y-4">
              <div className="p-4 bg-slate-50 dark:bg-slate-800 rounded-lg">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-sm font-medium text-slate-700 dark:text-slate-300">实时持仓监控</span>
                  <span className="inline-flex items-center gap-1 text-xs text-green-500">
                    <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse"></span>
                    运行中
                  </span>
                </div>
                <p className="text-xs text-slate-500 dark:text-slate-400">
                  默认开启，按 设置-通用 中的刷新间隔自动更新持仓行情
                </p>
              </div>

              <div className="p-4 bg-slate-50 dark:bg-slate-800 rounded-lg">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-sm font-medium text-slate-700 dark:text-slate-300">AI智能再平衡</span>
                </div>
                <p className="text-xs text-slate-500 dark:text-slate-400 mb-3">
                  CIO智能体将分析市场状态，量化研究员选股，风控师评估，交易员执行
                </p>
                <button
                  onClick={handleRebalance}
                  disabled={refreshing}
                  className="w-full py-2 rounded-lg text-sm font-medium bg-indigo-500 hover:bg-indigo-600 text-white transition-colors disabled:opacity-50"
                >
                  <ArrowRightLeft className="w-4 h-4 mr-1 inline" />
                  触发再平衡
                </button>
              </div>

              <div className="p-4 bg-slate-50 dark:bg-slate-800 rounded-lg">
                <div className="flex items-center justify-between mb-2">
                  <span className="text-sm font-medium text-slate-700 dark:text-slate-300">快速刷新</span>
                </div>
                <p className="text-xs text-slate-500 dark:text-slate-400 mb-3">
                  立即获取实时行情并更新持仓价格
                </p>
                <button
                  onClick={() => loadData()}
                  disabled={refreshing}
                  className="w-full py-2 rounded-lg text-sm font-medium bg-slate-200 dark:bg-slate-700 text-slate-700 dark:text-slate-200 hover:bg-slate-300 dark:hover:bg-slate-600 transition-colors disabled:opacity-50"
                >
                  <RefreshCw className={`w-4 h-4 mr-1 inline ${refreshing ? 'animate-spin' : ''}`} />
                  {refreshing ? '刷新中...' : '刷新行情'}
                </button>
              </div>
            </div>
          </div>
        </div>

        {/* 订单队列（orders 临时表）：待确认 / 已成交 / 已取消 */}
        <div className="card overflow-hidden mt-6">
          <div className="p-5 border-b border-slate-200 dark:border-slate-700 flex items-center justify-between flex-wrap gap-3">
            <h3 className="font-medium text-slate-800 dark:text-slate-100">订单队列 · 共 {orders.length} 条</h3>
              <div className="flex items-center gap-2">
                <div className="flex rounded-lg overflow-hidden border border-slate-200 dark:border-slate-700 text-xs">
                  <button
                    onClick={() => { setOrdersFilter('pending'); loadOrders(false) }}
                    className={`px-3 py-1.5 font-medium transition-colors ${
                      ordersFilter === 'pending'
                        ? 'bg-indigo-500 text-white'
                        : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-300'
                    }`}
                  >
                    未成交
                  </button>
                  <button
                    onClick={() => { setOrdersFilter('all'); loadOrders(true) }}
                    className={`px-3 py-1.5 font-medium transition-colors ${
                      ordersFilter === 'all'
                        ? 'bg-indigo-500 text-white'
                        : 'bg-white dark:bg-slate-800 text-slate-600 dark:text-slate-300'
                    }`}
                  >
                    全部
                  </button>
                </div>
                <button
                  onClick={() => loadOrders(ordersFilter === 'all')}
                  className="p-1.5 rounded-lg border border-slate-200 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800 text-slate-500"
                  title="刷新订单"
                >
                  <RefreshCw className="w-4 h-4" />
                </button>
              </div>
            </div>
            <div className="overflow-x-auto">
              <table className="w-full">
                <thead>
                  <tr className="text-left text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-700">
                    <th className="px-5 py-3 font-medium">提交时间</th>
                    <th className="px-5 py-3 font-medium">方向</th>
                    <th className="px-5 py-3 font-medium">代码</th>
                    <th className="px-5 py-3 font-medium">股票</th>
                    <th className="px-5 py-3 font-medium">数量</th>
                    <th className="px-5 py-3 font-medium">价格</th>
                    <th className="px-5 py-3 font-medium">金额</th>
                    <th className="px-5 py-3 font-medium">状态</th>
                  </tr>
                </thead>
                <tbody>
                  {orders.length > 0 ? (
                    orders.map((o) => {
                      const isBuy = o.side === 'BUY'
                      const statusMeta =
                        o.status === 'pending'
                          ? { text: '排队中', cls: 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400' }
                          : o.status === 'filled'
                            ? { text: '已成交', cls: 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' }
                            : { text: '已取消', cls: 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-400' }
                      return (
                        <tr
                          key={o.orderId}
                          className="border-b border-slate-100 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800/50 transition-colors"
                        >
                          <td className="px-5 py-3 text-sm text-slate-600 dark:text-slate-400">
                            {new Date(o.submittedAt || o.createdAt).toLocaleString('zh-CN')}
                          </td>
                          <td className="px-5 py-3">
                            <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${
                              isBuy
                                ? 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
                                : 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
                            }`}>
                              {isBuy ? '买入' : '卖出'}
                            </span>
                          </td>
                          <td className="px-5 py-3 font-mono text-sm">{o.instrumentId}</td>
                          <td className="px-5 py-3 text-sm">{o.stockName}</td>
                          <td className="px-5 py-3">{o.quantity.toLocaleString()}</td>
                          <td className="px-5 py-3">¥{o.price.toFixed(2)}</td>
                          <td className="px-5 py-3 font-semibold">{formatCurrency(o.amount)}</td>
                          <td className="px-5 py-3">
                            <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${statusMeta.cls}`}>
                              {statusMeta.text}
                            </span>
                          </td>
                        </tr>
                      )
                    })
                  ) : (
                    <tr>
                      <td colSpan={8} className="text-center py-8 text-slate-400 dark:text-slate-500">
                        暂无订单
                      </td>
                    </tr>
                  )}
                </tbody>
              </table>
            </div>
          </div>
        </>
      )}
      {activeTab === 'profit' && (
        <div className="space-y-4">
          {/* 盈亏分析概览卡片 */}
          <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
            <div className="card p-4">
              <div className="text-xs text-slate-500 mb-1">总资产</div>
              <div className="text-lg font-bold text-slate-800 dark:text-slate-100">
                {formatCurrency(latestDailyStat?.totalAssets || totalAssets)}
              </div>
              {latestDailyStat?.date && (
                <div className="text-xs text-slate-400 mt-1">{latestDailyStat.date}</div>
              )}
            </div>
            <div className="card p-4">
              <div className="text-xs text-slate-500 mb-1">累计收益</div>
              <div className={`text-lg font-bold ${(latestDailyStat?.totalReturn || totalReturn) >= 0 ? 'text-green-600' : 'text-red-600'}`}>
                {formatPercent(latestDailyStat?.totalReturn || totalReturn)}
              </div>
              <div className={`text-sm ${(latestDailyStat?.totalPnL || totalPnL) >= 0 ? 'text-green-500' : 'text-red-500'}`}>
                {(latestDailyStat?.totalPnL || totalPnL) >= 0 ? '+' : ''}{formatCurrency(latestDailyStat?.totalPnL || totalPnL)}
              </div>
            </div>
            <div className="card p-4">
              <div className="text-xs text-slate-500 mb-1">当日盈亏</div>
              <div className={`text-lg font-bold ${(latestDailyStat?.dailyPnL || dailyPnL) >= 0 ? 'text-green-600' : 'text-red-600'}`}>
                {(latestDailyStat?.dailyPnL || dailyPnL) >= 0 ? '+' : ''}{formatCurrency(latestDailyStat?.dailyPnL || dailyPnL)}
              </div>
              <div className={`text-sm ${(latestDailyStat?.dailyReturn || 0) >= 0 ? 'text-green-500' : 'text-red-500'}`}>
                {formatPercent(latestDailyStat?.dailyReturn || 0)}
              </div>
            </div>
            <div className="card p-4">
              <div className="text-xs text-slate-500 mb-1">持仓数量</div>
              <div className="text-lg font-bold text-slate-800 dark:text-slate-100">
                {latestDailyStat?.positionsCount || positionsCount} 只
              </div>
              <div className="text-xs text-slate-400 mt-1">
                市值 {formatCurrency(latestDailyStat?.marketValue || portfolio?.totalMarketValue || 0)}
              </div>
            </div>
          </div>

          {/* 记账对账（财务流水账：期初 / 临时账 / 明细账 / 总账） */}
          {portfolio?.ledger && (
            <div className="card p-5">
              <div className="flex items-center justify-between mb-4 flex-wrap gap-2">
                <div className="flex items-center gap-2">
                  <Activity className="w-5 h-5 text-cyan-500" />
                  <span className="font-medium text-slate-800 dark:text-slate-100">记账对账（财务流水账）</span>
                </div>
                <span className={`inline-flex items-center gap-1 px-2.5 py-1 rounded-lg text-xs font-medium ${
                  portfolio.ledger.reconciled
                    ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
                    : 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
                }`}>
                  {portfolio.ledger.reconciled ? '✓ 四账对平' : '⚠ 存在差异'}
                </span>
              </div>

              <div className="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
                <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <div className="text-xs text-slate-400 mb-1">期初账 · 初始资金</div>
                  <div className="font-semibold text-slate-800 dark:text-slate-100">
                    {formatCurrency(portfolio.ledger.opening_capital)}
                  </div>
                </div>
                <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <div className="text-xs text-slate-400 mb-1">临时账 · 挂单占用</div>
                  <div className="font-semibold text-slate-800 dark:text-slate-100">
                    {formatCurrency(portfolio.ledger.temp.reserved_buy)}
                  </div>
                  <div className="text-xs text-slate-400 mt-0.5">
                    {portfolio.ledger.temp.pending_orders} 笔待确认
                  </div>
                </div>
                <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <div className="text-xs text-slate-400 mb-1">明细账 · 成交流水</div>
                  <div className="font-semibold text-slate-800 dark:text-slate-100">
                    {portfolio.ledger.detail.buy_count}买 / {portfolio.ledger.detail.sell_count}卖
                  </div>
                  <div className="text-xs text-slate-400 mt-0.5">
                    买入 {formatCurrency(portfolio.ledger.detail.buy_net)} · 卖出 {formatCurrency(portfolio.ledger.detail.sell_net)}
                  </div>
                </div>
                <div className="p-3 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <div className="text-xs text-slate-400 mb-1">现金 · 明细账推导</div>
                  <div className="font-semibold text-slate-800 dark:text-slate-100">
                    {formatCurrency(portfolio.ledger.cash.derived)}
                  </div>
                  <div className="text-xs text-slate-400 mt-0.5">
                    记录 {formatCurrency(portfolio.ledger.cash.on_record)}
                  </div>
                </div>
              </div>

              {portfolio.ledger.general.date && (
                <div className="mt-3 p-3 rounded-lg bg-slate-50 dark:bg-slate-800/50 text-sm flex flex-wrap gap-x-6 gap-y-1">
                  <span className="text-xs text-slate-400">总账 · {portfolio.ledger.general.date} 结算</span>
                  <span>总资产 <b className="text-slate-800 dark:text-slate-100">{formatCurrency(portfolio.ledger.general.total_assets)}</b></span>
                  <span className={portfolio.ledger.general.total_pnl >= 0 ? 'text-green-600' : 'text-red-600'}>
                    累计盈亏 {portfolio.ledger.general.total_pnl >= 0 ? '+' : ''}{formatCurrency(portfolio.ledger.general.total_pnl)}
                  </span>
                  <span className={portfolio.ledger.general.daily_pnl >= 0 ? 'text-green-600' : 'text-red-600'}>
                    今日盈亏 {portfolio.ledger.general.daily_pnl >= 0 ? '+' : ''}{formatCurrency(portfolio.ledger.general.daily_pnl)}
                  </span>
                </div>
              )}

              {!portfolio.ledger.reconciled && portfolio.ledger.issues?.length > 0 && (
                <div className="mt-3 p-3 rounded-lg bg-red-50 dark:bg-red-900/20 text-xs text-red-600 dark:text-red-400">
                  {portfolio.ledger.issues.map((it, idx) => (
                    <div key={idx}>· {it}</div>
                  ))}
                </div>
              )}
            </div>
          )}

          {/* 时间周期选择器 */}
          <div className="card p-4">
            <div className="flex items-center justify-between mb-4">
              <div className="flex items-center gap-2">
                <LineChart className="w-5 h-5 text-indigo-500" />
                <span className="font-medium text-slate-800 dark:text-slate-100">收益走势</span>
              </div>
              <button
                onClick={handleRecordSnapshot}
                disabled={refreshing}
                className="flex items-center gap-1 px-3 py-1.5 text-xs font-medium bg-indigo-500 hover:bg-indigo-600 text-white rounded-lg transition-colors disabled:opacity-50"
              >
                <RefreshCw className={`w-3.5 h-3.5 ${refreshing ? 'animate-spin' : ''}`} />
                记录今日快照
              </button>
            </div>
            <div className="flex items-center gap-1 bg-slate-100 dark:bg-slate-800 rounded-lg p-1">
              {([
                { key: '1', label: '昨日' },
                { key: '7', label: '近一周' },
                { key: '30', label: '近一月' },
                { key: '90', label: '近三月' },
                { key: '365', label: '近一年' },
              ] as const).map((period) => (
                <button
                  key={period.key}
                  onClick={() => setProfitPeriod(period.key)}
                  className={`flex-1 px-3 py-1.5 rounded-md text-xs font-medium transition-all ${
                    profitPeriod === period.key
                      ? 'bg-white dark:bg-slate-700 shadow text-indigo-600 dark:text-indigo-400'
                      : 'text-slate-500 dark:text-slate-400 hover:text-slate-700'
                  }`}
                >
                  {period.label}
                </button>
              ))}
            </div>
          </div>

          {/* 累计收益曲线 */}
          <div className="card p-5">
            <div className="flex items-center gap-2 mb-4">
              <TrendingUp className="w-5 h-5 text-green-500" />
              <span className="font-medium text-slate-800 dark:text-slate-100">累计资产走势</span>
            </div>
            <div className="h-64">
              {profitLoading ? (
                <div className="flex items-center justify-center h-full text-slate-400">
                  <RefreshCw className="w-5 h-5 animate-spin mr-2" />
                  加载中...
                </div>
              ) : profitHistory.length > 0 ? (
                <ResponsiveContainer width="100%" height="100%">
                  <AreaChart data={profitHistory}>
                    <defs>
                      <linearGradient id="totalAssetsGradient" x1="0" y1="0" x2="0" y2="1">
                        <stop offset="5%" stopColor="#6366f1" stopOpacity={0.3}/>
                        <stop offset="95%" stopColor="#6366f1" stopOpacity={0}/>
                      </linearGradient>
                    </defs>
                    <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" strokeOpacity={0.5} />
                    <XAxis dataKey="date" tick={{ fontSize: 11 }} tickFormatter={(v) => v?.substring(5)} />
                    <YAxis tick={{ fontSize: 11 }} tickFormatter={(v) => `¥${(v / 10000).toFixed(1)}万`} />
                    <Tooltip
                      formatter={(value: number, name: string) => [
                        name === 'totalAssets' ? formatCurrency(value) : value,
                        name === 'totalAssets' ? '总资产' : name
                      ]}
                      labelFormatter={(label) => `日期: ${label}`}
                      contentStyle={{ borderRadius: '8px', border: '1px solid #e5e7eb' }}
                    />
                    <Area
                      type="monotone"
                      dataKey="totalAssets"
                      stroke="#6366f1"
                      strokeWidth={2}
                      fillOpacity={1}
                      fill="url(#totalAssetsGradient)"
                    />
                  </AreaChart>
                </ResponsiveContainer>
              ) : (
                <div className="flex flex-col items-center justify-center h-full text-slate-400">
                  <Calendar className="w-10 h-10 mb-2 opacity-50" />
                  <p className="text-sm">暂无历史数据</p>
                  <p className="text-xs mt-1">系统会在每日收盘后自动记录持仓快照</p>
                </div>
              )}
            </div>
          </div>

          {/* 每日盈亏柱状图 */}
          <div className="card p-5">
            <div className="flex items-center gap-2 mb-4">
              <BarChart3 className="w-5 h-5 text-orange-500" />
              <span className="font-medium text-slate-800 dark:text-slate-100">每日盈亏</span>
            </div>
            <div className="h-56">
              {profitLoading ? (
                <div className="flex items-center justify-center h-full text-slate-400">
                  <RefreshCw className="w-5 h-5 animate-spin mr-2" />
                  加载中...
                </div>
              ) : profitHistory.length > 0 ? (
                <ResponsiveContainer width="100%" height="100%">
                  <BarChart data={profitHistory}>
                    <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" strokeOpacity={0.5} />
                    <XAxis dataKey="date" tick={{ fontSize: 11 }} tickFormatter={(v) => v?.substring(5)} />
                    <YAxis tick={{ fontSize: 11 }} tickFormatter={(v) => `¥${(v / 1000).toFixed(0)}k`} />
                    <Tooltip
                      formatter={(value: number) => [formatCurrency(value), '当日盈亏']}
                      labelFormatter={(label) => `日期: ${label}`}
                      contentStyle={{ borderRadius: '8px', border: '1px solid #e5e7eb' }}
                    />
                    <Bar
                      dataKey="dailyPnL"
                      radius={[4, 4, 0, 0]}
                      fill="#10b981"
                    />
                  </BarChart>
                </ResponsiveContainer>
              ) : (
                <div className="flex flex-col items-center justify-center h-full text-slate-400">
                  <Activity className="w-10 h-10 mb-2 opacity-50" />
                  <p className="text-sm">暂无每日盈亏数据</p>
                </div>
              )}
            </div>
          </div>

          {/* 净值 vs 沪深300基准 卡片 */}
          <div className="card p-5">
            <div className="flex items-center gap-2 mb-2">
              <TrendingUp className="w-5 h-5 text-indigo-500" />
              <span className="font-medium text-slate-800 dark:text-slate-100">净值 vs 沪深300（业绩基准）</span>
            </div>
            {navBenchmarkData?.summary?.currentNav != null && (
              <div className="grid grid-cols-2 md:grid-cols-5 gap-2 mb-4 text-sm">
                <div className="p-2 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <div className="text-xs text-slate-400">当前净值(NAV)</div>
                  <div className="font-semibold text-slate-800 dark:text-slate-100">{navBenchmarkData.summary.currentNav}</div>
                </div>
                <div className="p-2 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <div className="text-xs text-slate-400">累计收益</div>
                  <div className={`font-semibold ${Number(navBenchmarkData.summary.totalReturn) >= 0 ? 'text-green-600' : 'text-red-600'}`}>
                    {Number(navBenchmarkData.summary.totalReturn) >= 0 ? '+' : ''}{navBenchmarkData.summary.totalReturn}%
                  </div>
                </div>
                <div className="p-2 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <div className="text-xs text-slate-400">基准累计收益</div>
                  <div className={`font-semibold ${Number(navBenchmarkData.summary.benchmarkReturn) >= 0 ? 'text-green-600' : 'text-red-600'}`}>
                    {Number(navBenchmarkData.summary.benchmarkReturn) >= 0 ? '+' : ''}{navBenchmarkData.summary.benchmarkReturn}%
                  </div>
                </div>
                <div className="p-2 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <div className="text-xs text-slate-400">超额收益(α)</div>
                  <div className={`font-semibold ${Number(navBenchmarkData.summary.alpha) >= 0 ? 'text-green-600' : 'text-red-600'}`}>
                    {Number(navBenchmarkData.summary.alpha) >= 0 ? '+' : ''}{navBenchmarkData.summary.alpha}%
                  </div>
                </div>
                <div className="p-2 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <div className="text-xs text-slate-400">最大回撤</div>
                  <div className="font-semibold text-red-600">-{navBenchmarkData.summary.maxDrawdown}%</div>
                </div>
              </div>
            )}
            <div className="h-64">
              {navBenchmarkData?.series?.length > 0 ? (
                <ResponsiveContainer width="100%" height="100%">
                  <RechartsLineChart data={navBenchmarkData.series}>
                    <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" strokeOpacity={0.5} />
                    <XAxis dataKey="date" tick={{ fontSize: 11 }} tickFormatter={(v) => v?.substring(5)} />
                    <YAxis tick={{ fontSize: 11 }} domain={['auto', 'auto']} />
                    <Tooltip
                      formatter={(value: number, name: string) => [
                        name === 'nav' ? Number(value).toFixed(4) : Number(value).toFixed(4),
                        name === 'nav' ? '组合净值' : '沪深300基准'
                      ]}
                      labelFormatter={(label) => `日期: ${label}`}
                      contentStyle={{ borderRadius: '8px', border: '1px solid #e5e7eb' }}
                    />
                    <Line type="monotone" dataKey="nav" name="nav" stroke="#6366f1" strokeWidth={2} dot={false} />
                    <Line type="monotone" dataKey="benchmarkNav" name="benchmarkNav" stroke="#f59e0b" strokeWidth={2} strokeDasharray="5 5" dot={false} />
                  </RechartsLineChart>
                </ResponsiveContainer>
              ) : (
                <div className="flex flex-col items-center justify-center h-full text-slate-400">
                  <Activity className="w-10 h-10 mb-2 opacity-50" />
                  <p className="text-sm">{navBenchmarkData?.summary?.message || '暂无净值数据，待日终结算后生成'}</p>
                </div>
              )}
            </div>
          </div>
          <div className="card p-5">
            <div className="flex items-center gap-2 mb-4">
              <LineChart className="w-5 h-5 text-purple-500" />
              <span className="font-medium text-slate-800 dark:text-slate-100">累计收益率 (%)</span>
            </div>
            <div className="h-56">
              {profitLoading ? (
                <div className="flex items-center justify-center h-full text-slate-400">
                  <RefreshCw className="w-5 h-5 animate-spin mr-2" />
                  加载中...
                </div>
              ) : profitHistory.length > 0 ? (
                <ResponsiveContainer width="100%" height="100%">
                  <RechartsLineChart data={profitHistory}>
                    <CartesianGrid strokeDasharray="3 3" stroke="#e5e7eb" strokeOpacity={0.5} />
                    <XAxis dataKey="date" tick={{ fontSize: 11 }} tickFormatter={(v) => v?.substring(5)} />
                    <YAxis tick={{ fontSize: 11 }} tickFormatter={(v) => `${v?.toFixed(2)}%`} />
                    <Tooltip
                      formatter={(value: number) => [`${value?.toFixed(2)}%`, '累计收益率']}
                      labelFormatter={(label) => `日期: ${label}`}
                      contentStyle={{ borderRadius: '8px', border: '1px solid #e5e7eb' }}
                    />
                    <Line
                      type="monotone"
                      dataKey="totalReturn"
                      stroke="#8b5cf6"
                      strokeWidth={2}
                      dot={{ r: 3, fill: '#8b5cf6' }}
                      activeDot={{ r: 5 }}
                    />
                  </RechartsLineChart>
                </ResponsiveContainer>
              ) : (
                <div className="flex flex-col items-center justify-center h-full text-slate-400">
                  <LineChart className="w-10 h-10 mb-2 opacity-50" />
                  <p className="text-sm">暂无收益率数据</p>
                </div>
              )}
            </div>
          </div>

          {/* 每日盈亏明细表 */}
          {profitHistory.length > 0 && (
            <div className="card p-5">
              <div className="flex items-center gap-2 mb-4">
                <Activity className="w-5 h-5 text-blue-500" />
                <span className="font-medium text-slate-800 dark:text-slate-100">每日盈亏明细</span>
              </div>
              <div className="overflow-x-auto">
                <table className="w-full">
                  <thead>
                    <tr className="text-left text-xs text-slate-500 dark:text-slate-400 border-b border-slate-200 dark:border-slate-700">
                      <th className="px-3 py-2 font-medium">日期</th>
                      <th className="px-3 py-2 font-medium text-right">总资产</th>
                      <th className="px-3 py-2 font-medium text-right">当日盈亏</th>
                      <th className="px-3 py-2 font-medium text-right">当日收益率</th>
                      <th className="px-3 py-2 font-medium text-right">累计收益率</th>
                      <th className="px-3 py-2 font-medium text-right">持仓数</th>
                    </tr>
                  </thead>
                  <tbody>
                    {pagedProfit.map((item, idx) => {
                      const isUp = (item.dailyPnL || 0) >= 0
                      return (
                        <tr key={idx} className="border-b border-slate-100 dark:border-slate-700 hover:bg-slate-50 dark:hover:bg-slate-800/50">
                          <td className="px-3 py-2 text-sm font-mono text-slate-700 dark:text-slate-300">{item.date}</td>
                          <td className="px-3 py-2 text-sm text-right font-medium">{formatCurrency(item.totalAssets)}</td>
                          <td className={`px-3 py-2 text-sm text-right font-medium ${isUp ? 'text-green-500' : 'text-red-500'}`}>
                            {isUp ? '+' : ''}{formatCurrency(item.dailyPnL)}
                          </td>
                          <td className={`px-3 py-2 text-sm text-right ${(item.dailyReturn || 0) >= 0 ? 'text-green-500' : 'text-red-500'}`}>
                            {(item.dailyReturn || 0) >= 0 ? '+' : ''}{(item.dailyReturn || 0).toFixed(2)}%
                          </td>
                          <td className={`px-3 py-2 text-sm text-right ${(item.totalReturn || 0) >= 0 ? 'text-green-500' : 'text-red-500'}`}>
                            {(item.totalReturn || 0) >= 0 ? '+' : ''}{(item.totalReturn || 0).toFixed(2)}%
                          </td>
                          <td className="px-3 py-2 text-sm text-right text-slate-600 dark:text-slate-400">{item.positionsCount}</td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
              <div className="mt-3">
                <TablePagination
                  total={reversedProfit.length}
                  currentPage={safeProfitPage}
                  totalPages={profitTotalPages}
                  onPageChange={setProfitPage}
                />
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
