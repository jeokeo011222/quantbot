import { useState, useCallback, useEffect, useRef } from 'react'
import { RefreshCw, Activity, TrendingUp, TrendingDown, AlertTriangle, Sparkles, X } from 'lucide-react'
import {
  getETFMonitorData,
  getETFMonitorKline,
  type ETFMonitorResponse,
  type ETFMonitorResult,
  type ETFIndicator,
  type ETFKlineResponse,
  type ETFKlinePoint,
} from '../services/etfMonitor'
import { useToastStore } from '../store/toastStore'

// A股习惯：红=风险/涨，绿=机会/跌
const STATE_STYLES: Record<string, { bg: string; text: string; border: string; glow: string }> = {
  red: {
    bg: 'bg-red-500',
    text: 'text-red-600 dark:text-red-400',
    border: 'border-red-500',
    glow: 'shadow-[0_0_12px_rgba(239,68,68,0.5)]',
  },
  green: {
    bg: 'bg-green-500',
    text: 'text-green-600 dark:text-green-400',
    border: 'border-green-500',
    glow: 'shadow-[0_0_12px_rgba(34,197,94,0.5)]',
  },
  gray: {
    bg: 'bg-slate-400',
    text: 'text-slate-500 dark:text-slate-400',
    border: 'border-slate-400',
    glow: '',
  },
}

const INDICATOR_META: Record<string, { short: string; icon: React.ComponentType<{ className?: string }> }> = {
  price_position: { short: '价格位置', icon: TrendingUp },
  share_flow: { short: '份额流向', icon: Activity },
  trade_direction: { short: '交易方向', icon: TrendingDown },
  turnover: { short: '成交额', icon: Activity },
  margin: { short: '融资', icon: Sparkles },
}

const VERDICT_STYLES: Record<string, { text: string; bg: string; border: string }> = {
  危险共振: { text: 'text-red-600 dark:text-red-400', bg: 'bg-red-50 dark:bg-red-900/20', border: 'border-red-200 dark:border-red-800' },
  机会共振: { text: 'text-green-600 dark:text-green-400', bg: 'bg-green-50 dark:bg-green-900/20', border: 'border-green-200 dark:border-green-800' },
  中性: { text: 'text-slate-600 dark:text-slate-300', bg: 'bg-slate-100 dark:bg-slate-800', border: 'border-slate-200 dark:border-slate-700' },
}

export default function ETFMonitor() {
  const [data, setData] = useState<ETFMonitorResponse | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<ETFMonitorResult | null>(null)
  const [kline, setKline] = useState<ETFKlineResponse | null>(null)
  const [klineLoading, setKlineLoading] = useState(false)
  const [klineError, setKlineError] = useState<string | null>(null)

  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)

  const loadData = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const result = await getETFMonitorData()
      setData(result)
    } catch (err) {
      console.error('ETF monitor failed:', err)
      // 显示真实错误信息（数据源/后端真实报错），便于定位问题
      const raw = err instanceof Error ? err.message : String(err)
      setError(raw || 'ETF监控数据获取失败，将在30秒后自动重试')
      // 弹窗警示 + 30 秒自动重试直到恢复（首次失败弹一次）
      useToastStore.getState().error(
        'ETF监控数据获取失败，将在30秒后自动重试',
        raw || ''
      )
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
      retryTimerRef.current = setTimeout(loadData, 30000)
    } finally {
      setIsLoading(false)
    }
  }, [])

  // 组件挂载时加载，卸载时清理自动重试定时器
  useEffect(() => {
    loadData()
    return () => {
      if (retryTimerRef.current) clearTimeout(retryTimerRef.current)
    }
  }, [loadData])

  const handleRefresh = async () => {
    await loadData()
  }

  // 点击矩阵单元格：在页面底部展示该ETF的K线 + 指标明细
  const handleSelect = useCallback(async (etf: ETFMonitorResult) => {
    setSelected(etf)
    setKline(null)
    setKlineError(null)
    setKlineLoading(true)
    try {
      const k = await getETFMonitorKline(etf.code)
      setKline(k)
    } catch (err) {
      console.error('ETF kline failed:', err)
      setKlineError(err instanceof Error ? err.message : String(err))
    } finally {
      setKlineLoading(false)
    }
  }, [])

  const handleClose = useCallback(() => {
    setSelected(null)
    setKline(null)
    setKlineError(null)
  }, [])

  return (
    <div className="bg-slate-50 dark:bg-slate-900 min-h-full">
      {/* 顶部标题区 */}
      <div className="px-6 py-4 border-b border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-purple-500 to-indigo-500 flex items-center justify-center">
              <Activity className="w-5 h-5 text-white" />
            </div>
            <div>
              <h1 className="text-xl font-bold text-slate-800 dark:text-slate-100 flex items-center gap-2">
                ETF监控
                <span className="inline-flex items-center gap-1 px-2 py-0.5 text-xs font-medium bg-rose-100 text-rose-700 dark:bg-rose-900/30 dark:text-rose-400 rounded">
                  <Sparkles className="w-3 h-3" />
                  三因子 → 五指标 → 共振
                </span>
              </h1>
              <p className="text-sm text-slate-500 dark:text-slate-400">
                资金行为三因子（价格位置 / 份额流向 / 交易方向）+ 市场情绪两维度（成交额热度 / 融资杠杆）· 手动刷新监控研究
              </p>
            </div>
          </div>
          <button
            onClick={handleRefresh}
            disabled={isLoading}
            className="flex items-center gap-2 px-4 py-2 rounded-lg text-sm font-medium transition-colors bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 disabled:cursor-not-allowed"
          >
            <RefreshCw className={`w-4 h-4 ${isLoading ? 'animate-spin' : ''}`} />
            {isLoading ? '刷新中...' : '手动刷新'}
          </button>
        </div>
      </div>

      <div className="p-6 space-y-6">
        {/* 错误提示 */}
        {error && (
          <div className="flex items-center gap-2 px-4 py-3 rounded-lg bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-sm text-red-700 dark:text-red-400">
            <AlertTriangle className="w-4 h-4 shrink-0" />
            {error}
          </div>
        )}

        {/* 首次加载提示 */}
        {!data && !isLoading && !error && (
          <div className="flex flex-col items-center justify-center py-20 text-center">
            <div className="w-16 h-16 rounded-2xl bg-slate-100 dark:bg-slate-800 flex items-center justify-center mb-4">
              <Activity className="w-8 h-8 text-slate-400" />
            </div>
            <h3 className="text-base font-semibold text-slate-700 dark:text-slate-200 mb-1">ETF 共振监控</h3>
            <p className="text-sm text-slate-500 dark:text-slate-400 max-w-md">
              点击「手动刷新」获取最新数据。系统从三个维度刻画资金行为，再叠加两个市场情绪维度，共五个指标各亮一盏灯，同色灯 ≥3 盏即触发共振。
            </p>
          </div>
        )}

        {data && (
          <>
            {/* 市场情绪区 */}
            <MarketSentimentBar data={data} />

            {/* 共振热力图矩阵 */}
            <ResonanceMatrix results={data.results} onSelect={handleSelect} />

            {/* 图例 */}
            <div className="flex items-center gap-4 text-xs text-slate-500 dark:text-slate-400">
              <span className="flex items-center gap-1.5">
                <span className="w-3 h-3 rounded-full bg-red-500" /> 红灯 = 风险 / 出货 / 过热
              </span>
              <span className="flex items-center gap-1.5">
                <span className="w-3 h-3 rounded-full bg-green-500" /> 绿灯 = 机会 / 吸筹 / 冷清
              </span>
              <span className="flex items-center gap-1.5">
                <span className="w-3 h-3 rounded-full bg-slate-400" /> 灰灯 = 中性
              </span>
              <span className="ml-auto text-xs text-slate-400">
                更新于 {data.generated_at}
              </span>
            </div>
          </>
        )}
      </div>

      {/* 底部详情区：K线图 + 指标明细（替代原弹窗） */}
      {selected && (
        <ETFDetailPanel
          etf={selected}
          kline={kline}
          klineLoading={klineLoading}
          klineError={klineError}
          onClose={handleClose}
        />
      )}
    </div>
  )
}

function MarketSentimentBar({ data }: { data: ETFMonitorResponse }) {
  const market = data.market
  const turnoverState = STATE_STYLES[market.turnover_state || 'gray']
  const marginState = STATE_STYLES[market.margin_state || 'gray']

  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
      <div className="rounded-xl border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 p-4">
        <div className="flex items-center justify-between mb-2">
          <div className="flex items-center gap-2">
            <span className={`w-2.5 h-2.5 rounded-full ${turnoverState.bg}`} />
            <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-200">成交额热度</h3>
          </div>
          <span className={`text-xs font-medium ${turnoverState.text}`}>
            {market.turnover_state === 'red' ? '过热' : market.turnover_state === 'green' ? '冷清' : '中性'}
          </span>
        </div>
        <div className="flex items-baseline gap-2">
          <span className="text-2xl font-bold text-slate-800 dark:text-slate-100">
            {market.turnover_percentile ? `${market.turnover_percentile.toFixed(0)}%` : '-'}
          </span>
          <span className="text-xs text-slate-500 dark:text-slate-400">分位</span>
          <span className="ml-auto text-sm text-slate-500 dark:text-slate-400">
            {market.turnover_value ? `${market.turnover_value.toFixed(0)}亿` : '-'}
          </span>
        </div>
        <div className="mt-2 h-1.5 rounded-full bg-slate-100 dark:bg-slate-700 overflow-hidden">
          <div
            className={`h-full rounded-full ${turnoverState.bg}`}
            style={{ width: `${Math.min(100, market.turnover_percentile || 0)}%` }}
          />
        </div>
        <p className="mt-2 text-xs text-slate-400">两市成交额 {market.date} · 60日滚动分位</p>
      </div>

      <div className="rounded-xl border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 p-4">
        <div className="flex items-center justify-between mb-2">
          <div className="flex items-center gap-2">
            <span className={`w-2.5 h-2.5 rounded-full ${marginState.bg}`} />
            <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-200">融资杠杆</h3>
          </div>
          <span className={`text-xs font-medium ${marginState.text}`}>
            {market.margin_state === 'red' ? '过热' : market.margin_state === 'green' ? '冷清' : '中性'}
          </span>
        </div>
        <div className="flex items-baseline gap-2">
          <span className="text-2xl font-bold text-slate-800 dark:text-slate-100">
            {market.margin_percentile ? `${market.margin_percentile.toFixed(0)}%` : '-'}
          </span>
          <span className="text-xs text-slate-500 dark:text-slate-400">分位</span>
          <span className="ml-auto text-sm text-slate-500 dark:text-slate-400">
            {market.margin_value ? `${market.margin_value.toFixed(0)}亿` : '-'}
          </span>
        </div>
        <div className="mt-2 h-1.5 rounded-full bg-slate-100 dark:bg-slate-700 overflow-hidden">
          <div
            className={`h-full rounded-full ${marginState.bg}`}
            style={{ width: `${Math.min(100, market.margin_percentile || 0)}%` }}
          />
        </div>
        <p className="mt-2 text-xs text-slate-400">融资余额 {market.date} · 60日滚动分位</p>
      </div>
    </div>
  )
}

const MATRIX_KEYS = ['price_position', 'share_flow', 'trade_direction', 'turnover', 'margin']

// 共振热力图矩阵：10 ETF × 5 指标，一眼识别共振状态
function ResonanceMatrix({
  results,
  onSelect,
}: {
  results: ETFMonitorResult[]
  onSelect: (etf: ETFMonitorResult) => void
}) {
  // 每列（指标）的红/绿灯统计
  const colStats = MATRIX_KEYS.map((key) => {
    let red = 0
    let green = 0
    for (const etf of results) {
      const ind = etf.indicators.find((i) => i.key === key)
      if (!ind) continue
      if (ind.state === 'red') red++
      if (ind.state === 'green') green++
    }
    return { key, red, green }
  })

  const totalResonance = results.filter((e) => e.verdict === '危险共振' || e.verdict === '机会共振').length

  return (
    <div className="rounded-xl border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800 p-4">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-200">共振热力图矩阵</h3>
          <span className="text-xs text-slate-400">点击任意单元格查看该 ETF 指标明细</span>
        </div>
        <span className="text-xs text-slate-500 dark:text-slate-400">
          共振 {totalResonance}/{results.length} 只
        </span>
      </div>

      <div className="overflow-x-auto">
        <div className="min-w-[560px]">
          {/* 表头 */}
          <div className="grid grid-cols-[1.4fr_repeat(5,1fr)_0.9fr] items-center gap-1 px-2 pb-2 border-b border-slate-100 dark:border-slate-700">
            <span className="text-xs font-medium text-slate-400">ETF</span>
            {MATRIX_KEYS.map((key) => {
              const meta = INDICATOR_META[key] || { short: key, icon: Activity }
              const stat = colStats.find((c) => c.key === key)
              return (
                <span key={key} className="flex flex-col items-center gap-0.5">
                  <span className="text-[11px] font-medium text-slate-500 dark:text-slate-400">{meta.short}</span>
                  <span className="text-[10px] text-slate-400">
                    {stat && stat.red > 0 && <span className="text-red-500">{stat.red}红</span>}
                    {stat && stat.red > 0 && stat.green > 0 && <span className="text-slate-300">/</span>}
                    {stat && stat.green > 0 && <span className="text-green-500">{stat.green}绿</span>}
                  </span>
                </span>
              )
            })}
            <span className="text-center text-[11px] font-medium text-slate-400">判定</span>
          </div>

          {/* 数据行 */}
          {results.map((etf) => {
            const isError = etf.verdict === '数据获取失败'
            const verdictStyle = VERDICT_STYLES[etf.verdict] || VERDICT_STYLES['中性']
            return (
              <div
                key={etf.code}
                className="grid grid-cols-[1.4fr_repeat(5,1fr)_0.9fr] items-center gap-1 px-2 py-1.5 border-b border-slate-50 dark:border-slate-800 hover:bg-slate-50 dark:hover:bg-slate-900/40 cursor-pointer transition-colors"
                onClick={() => onSelect(etf)}
              >
                <div className="min-w-0">
                  <div className="text-xs font-medium text-slate-700 dark:text-slate-200 truncate">{etf.name}</div>
                  <div className="text-[10px] text-slate-400">{etf.code}</div>
                </div>
                {MATRIX_KEYS.map((key) => {
                  const ind = etf.indicators.find((i) => i.key === key)
                  if (!ind) {
                    return <div key={key} className="flex items-center justify-center"><span className="w-4 h-4 rounded-full bg-slate-200 dark:bg-slate-700" /></div>
                  }
                  const style = STATE_STYLES[ind.state] || STATE_STYLES.gray
                  return (
                    <div key={key} className="flex flex-col items-center gap-0.5">
                      <div
                        className={`w-[18px] h-[18px] rounded-full ${style.bg} ${style.glow}`}
                        title={`${ind.name}：${ind.display}（${ind.reason}）`}
                      />
                      <span className={`text-[9px] leading-none ${style.text}`}>{ind.display}</span>
                    </div>
                  )
                })}
                <div className="flex items-center justify-center">
                  {isError ? (
                    <span className="text-[10px] text-red-500">失败</span>
                  ) : (
                    <span className={`inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-semibold border ${verdictStyle.bg} ${verdictStyle.text} ${verdictStyle.border}`}>
                      {etf.verdict}
                    </span>
                  )}
                </div>
              </div>
            )
          })}

          {/* 列汇总 */}
          <div className="grid grid-cols-[1.4fr_repeat(5,1fr)_0.9fr] items-center gap-1 px-2 pt-2">
            <span className="text-[11px] font-medium text-slate-400">红灯合计</span>
            {colStats.map((c) => (
              <span key={c.key} className="text-center text-[11px] font-semibold text-red-500">{c.red}</span>
            ))}
            <span />
            <span className="text-[11px] font-medium text-slate-400">绿灯合计</span>
            {colStats.map((c) => (
              <span key={c.key} className="text-center text-[11px] font-semibold text-green-500">{c.green}</span>
            ))}
            <span />
          </div>
        </div>
      </div>
    </div>
  )
}

function IndicatorLamp({ ind }: { ind: ETFIndicator }) {
  const style = STATE_STYLES[ind.state] || STATE_STYLES.gray
  const meta = INDICATOR_META[ind.key] || { short: ind.name, icon: Activity }
  const Icon = meta.icon
  const stateLabel = ind.state === 'red' ? '红灯' : ind.state === 'green' ? '绿灯' : '灰灯'

  return (
    <div className="flex flex-col items-center gap-1 group" title={`${ind.name}：${ind.display}（${stateLabel}）`}>
      <div className={`relative w-8 h-8 rounded-full ${style.bg} ${style.glow} flex items-center justify-center`}>
        <Icon className="w-4 h-4 text-white" />
      </div>
      <span className="text-[10px] text-slate-500 dark:text-slate-400 leading-none">{meta.short}</span>
      <span className={`text-[10px] font-medium leading-none ${style.text}`}>{ind.display}</span>
    </div>
  )
}

// 指标金融含义（固定说明）
const INDICATOR_MEANING: Record<string, string> = {
  price_position: '衡量ETF当前价格在近60个交易日价格区间中的相对位置，反映当前处于高位还是低位。',
  share_flow: '反映ETF份额的申购/赎回方向，即场外资金通过一级市场申赎对ETF的净流入或净流出。',
  trade_direction: '结合价格位置与成交量变化，判断主力资金是在低位放量吸筹还是高位放量出货。',
  turnover: '反映沪深两市整体成交活跃度，衡量市场情绪是冷清还是过热。',
  margin: '反映融资余额水平，衡量杠杆资金参与市场的热度。',
}

interface KlineStats {
  maxHigh: number
  minLow: number
  lastClose: number
  periodReturn: number // 近3个月涨跌幅 %
  position: number // 当前价格在近60日区间的位置 %
  volRatio: number // 最新成交量 / 近3个月平均成交量
  rising: boolean // 近5日是否上涨
}

// 结合K线图计算统计量
function computeKlineStats(bars: ETFKlinePoint[]): KlineStats | null {
  if (!bars || bars.length === 0) return null
  const highs = bars.map((b) => b.high)
  const lows = bars.map((b) => b.low)
  const closes = bars.map((b) => b.close)
  const volumes = bars.map((b) => b.volume)
  const maxHigh = Math.max(...highs)
  const minLow = Math.min(...lows)
  const lastClose = closes[closes.length - 1]
  const firstClose = closes[0]
  const periodReturn = ((lastClose - firstClose) / firstClose) * 100
  const position = ((lastClose - minLow) / (maxHigh - minLow || 1)) * 100
  const avgVol = volumes.reduce((a, b) => a + b, 0) / volumes.length
  const lastVol = volumes[volumes.length - 1]
  const volRatio = avgVol > 0 ? lastVol / avgVol : 0
  const recent5 = closes.slice(-5)
  const rising = recent5[recent5.length - 1] >= recent5[0]
  return { maxHigh, minLow, lastClose, periodReturn, position, volRatio, rising }
}

// 结合K线图生成数值解释
function buildIndicatorAnalysis(ind: ETFIndicator, stats: KlineStats | null): string {
  if (!stats) return ind.reason
  const pos = stats.position
  const posLabel = pos <= 40 ? '低位' : pos >= 70 ? '高位' : '中位'
  const trend = stats.periodReturn >= 0 ? '上行' : '下行'
  const trendPct = `${Math.abs(stats.periodReturn).toFixed(1)}%`
  const volDesc =
    stats.volRatio >= 1.5
      ? `放量（最新成交量为近3个月均量的 ${stats.volRatio.toFixed(1)} 倍）`
      : stats.volRatio <= 0.7
        ? `缩量（最新成交量为近3个月均量的 ${stats.volRatio.toFixed(1)} 倍）`
        : `量能平稳（最新成交量为近3个月均量的 ${stats.volRatio.toFixed(1)} 倍）`

  switch (ind.key) {
    case 'price_position':
      return `当前价 ${stats.lastClose.toFixed(3)} 位于近60日区间（${stats.minLow.toFixed(3)} ~ ${stats.maxHigh.toFixed(3)}）的 ${pos.toFixed(0)}% 位置（${posLabel}）。K线图显示近3个月整体${trend}（累计${trendPct}），${
        ind.state === 'green' ? '与指示灯一致（低位机会）' : ind.state === 'red' ? '与指示灯一致（高位风险）' : '指示灯为中性'
      }。`
    case 'share_flow':
      return `份额流向概率 ${ind.display}，反映场外资金${
        ind.state === 'green' ? '净申购（资金流入）' : ind.state === 'red' ? '净赎回（资金流出）' : '申赎平衡'
      }。结合K线图，近3个月价格${trend}（累计${trendPct}），${
        ind.state === 'green' ? '资金流入与价格企稳/上行相互印证' : ind.state === 'red' ? '资金流出与价格承压相互印证' : '资金面与价格走势需进一步观察'
      }。`
    case 'trade_direction':
      return `结合K线图，当前价处于近60日区间的 ${pos.toFixed(0)}% 位置（${posLabel}），最新${volDesc}。${
        ind.state === 'green' ? '低位放量吸筹特征明显，资金逢低承接' : ind.state === 'red' ? '高位放量出货特征明显，资金逢高派发' : '量价关系中性，方向不明'
      }。`
    case 'turnover':
      return `两市成交额分位 ${ind.display}，${
        ind.state === 'green' ? '市场成交冷清、情绪低迷，往往是阶段性底部区域特征' : ind.state === 'red' ? '市场成交过热、情绪亢奋，需警惕短期回调风险' : '市场成交处于中性水平'
      }。该ETF近3个月价格${trend}（累计${trendPct}），可结合大盘冷热判断ETF所处环境。`
    case 'margin':
      return `融资余额分位 ${ind.display}，${
        ind.state === 'green' ? '杠杆资金参与度低，市场杠杆出清较充分' : ind.state === 'red' ? '杠杆资金参与度高、市场杠杆拥挤，波动风险加大' : '杠杆资金参与度中性'
      }。该ETF近3个月价格${trend}（累计${trendPct}），杠杆环境可作为风险偏好的参考。`
    default:
      return ind.reason
  }
}

// 近3个月K线图（SVG蜡烛图，A股习惯：红涨绿跌）
function KlineChart({ bars }: { bars: ETFKlinePoint[] }) {
  const width = 760
  const height = 280
  const padL = 54
  const padR = 14
  const padT = 14
  const padB = 24
  const volH = 44
  const chartH = height - padT - padB - volH - 10
  const volTop = padT + chartH + 10

  if (!bars || bars.length === 0) {
    return <div className="text-sm text-slate-400 py-10 text-center">暂无K线数据</div>
  }

  const minLow = Math.min(...bars.map((b) => b.low))
  const maxHigh = Math.max(...bars.map((b) => b.high))
  const maxVol = Math.max(...bars.map((b) => b.volume)) || 1
  const range = maxHigh - minLow || 1
  const plotW = width - padL - padR
  const step = plotW / bars.length
  const bodyW = Math.max(2, Math.min(9, step * 0.62))

  const x = (i: number) => padL + i * step + step / 2
  const y = (p: number) => padT + ((maxHigh - p) / range) * chartH
  const volY = (v: number) => volTop + (1 - v / maxVol) * volH

  // 水平网格线（5条）
  const gridLines = [0, 1, 2, 3, 4].map((i) => {
    const p = minLow + (range * i) / 4
    return { yy: y(p), label: p.toFixed(3) }
  })

  // X轴标签（约6个，去重）
  const labelIdx = Array.from(
    new Set([0, Math.floor(bars.length / 5), Math.floor((bars.length * 2) / 5), Math.floor((bars.length * 3) / 5), Math.floor((bars.length * 4) / 5), bars.length - 1])
  )

  return (
    <svg viewBox={`0 0 ${width} ${height}`} className="w-full h-auto" role="img" aria-label="近3个月K线图">
      {/* 网格线 + 价格标签 */}
      {gridLines.map((g, i) => (
        <g key={i}>
          <line x1={padL} y1={g.yy} x2={width - padR} y2={g.yy} stroke="currentColor" strokeOpacity="0.08" strokeDasharray="3 3" />
          <text x={padL - 5} y={g.yy + 3} textAnchor="end" fontSize="9" fill="currentColor" fillOpacity="0.5">
            {g.label}
          </text>
        </g>
      ))}

      {/* K线 + 成交量 */}
      {bars.map((b, i) => {
        const up = b.close >= b.open
        const color = up ? '#ef4444' : '#22c55e' // A股：红涨绿跌
        const cx = x(i)
        const yOpen = y(b.open)
        const yClose = y(b.close)
        const top = Math.min(yOpen, yClose)
        const h = Math.max(1, Math.abs(yClose - yOpen))
        const vh = volTop + volH - volY(b.volume)
        return (
          <g key={i}>
            <line x1={cx} y1={y(b.high)} x2={cx} y2={y(b.low)} stroke={color} strokeWidth="1" />
            <rect x={cx - bodyW / 2} y={top} width={bodyW} height={h} fill={color} />
            <rect x={cx - bodyW / 2} y={volY(b.volume)} width={bodyW} height={vh} fill={color} opacity="0.28" />
          </g>
        )
      })}

      {/* X轴日期标签 */}
      {labelIdx.map((i) => (
        <text key={i} x={x(i)} y={height - 6} textAnchor="middle" fontSize="9" fill="currentColor" fillOpacity="0.5">
          {bars[i].date.slice(5)}
        </text>
      ))}
    </svg>
  )
}

// 底部详情区：K线图 + 指标明细
function ETFDetailPanel({
  etf,
  kline,
  klineLoading,
  klineError,
  onClose,
}: {
  etf: ETFMonitorResult
  kline: ETFKlineResponse | null
  klineLoading: boolean
  klineError: string | null
  onClose: () => void
}) {
  const verdictStyle = VERDICT_STYLES[etf.verdict] || VERDICT_STYLES['中性']
  const isError = etf.verdict === '数据获取失败'
  const klineStats = kline ? computeKlineStats(kline.bars) : null

  return (
    <div className="border-t border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800">
      <div className="max-w-[1400px] mx-auto p-6 space-y-5">
        {/* 头部 */}
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <h3 className="text-base font-bold text-slate-800 dark:text-slate-100">
              {etf.name} <span className="text-sm font-normal text-slate-400">{etf.code}</span>
            </h3>
            <span className="text-xs text-slate-400">{etf.index} · {etf.date}</span>
            {!isError && (
              <span className="text-base font-bold text-slate-800 dark:text-slate-100">{etf.price.toFixed(3)}</span>
            )}
            {!isError && (
              <span className={`text-sm font-semibold ${etf.change_pct >= 0 ? 'text-red-600 dark:text-red-400' : 'text-green-600 dark:text-green-400'}`}>
                {etf.change_pct >= 0 ? '+' : ''}{etf.change_pct.toFixed(2)}%
              </span>
            )}
            <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-semibold border ${verdictStyle.bg} ${verdictStyle.text} ${verdictStyle.border}`}>
              {etf.verdict}
            </span>
          </div>
          <button
            onClick={onClose}
            className="flex items-center gap-1 px-2.5 py-1.5 rounded-lg text-xs text-slate-500 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-700 transition-colors"
          >
            <X className="w-3.5 h-3.5" /> 收起
          </button>
        </div>

        {/* K线图 */}
        <div className="rounded-xl border border-slate-200 dark:border-slate-700 bg-slate-50 dark:bg-slate-900/50 p-4">
          <div className="flex items-center justify-between mb-2">
            <h4 className="text-sm font-semibold text-slate-700 dark:text-slate-200">近3个月K线</h4>
            <span className="text-xs text-slate-400">红涨绿跌 · 数据源：行情数据库</span>
          </div>
          {klineLoading ? (
            <div className="flex items-center justify-center py-14 text-sm text-slate-400">
              <RefreshCw className="w-4 h-4 animate-spin mr-2" /> K线加载中...
            </div>
          ) : klineError ? (
            <div className="flex items-center gap-2 py-10 text-sm text-red-600 dark:text-red-400">
              <AlertTriangle className="w-4 h-4" /> {klineError}
            </div>
          ) : kline ? (
            <div className="text-slate-700 dark:text-slate-300">
              <KlineChart bars={kline.bars} />
            </div>
          ) : (
            <div className="text-sm text-slate-400 py-10 text-center">暂无K线数据</div>
          )}
        </div>

        {/* 指标明细 */}
        {isError ? (
          <div className="flex items-center gap-2 text-sm text-red-600 dark:text-red-400">
            <AlertTriangle className="w-4 h-4" /> 该ETF数据获取失败，请稍后重试
          </div>
        ) : (
          <div className="space-y-4">
            {/* 五灯汇总 */}
            <div className="grid grid-cols-5 gap-2">
              {etf.indicators.map((ind) => (
                <IndicatorLamp key={ind.key} ind={ind} />
              ))}
            </div>

            {/* 共振判定 */}
            <div className={`flex items-center justify-between px-3 py-2 rounded-lg border ${verdictStyle.bg} ${verdictStyle.border}`}>
              <span className={`text-sm font-semibold ${verdictStyle.text}`}>共振判定</span>
              <span className={`text-sm font-bold ${verdictStyle.text}`}>{etf.verdict}</span>
            </div>

            {/* 指标明细 */}
            <div className="space-y-2">
              {etf.indicators.map((ind) => (
                <div
                  key={ind.key}
                  className="px-3 py-2.5 rounded-lg bg-slate-50 dark:bg-slate-900/50 border border-slate-100 dark:border-slate-700"
                >
                  <div className="flex items-start gap-3">
                    <span className={`mt-1 w-2.5 h-2.5 rounded-full shrink-0 ${(STATE_STYLES[ind.state] || STATE_STYLES.gray).bg}`} />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium text-slate-700 dark:text-slate-200">{ind.name}</span>
                        <span className={`text-sm font-semibold ${(STATE_STYLES[ind.state] || STATE_STYLES.gray).text}`}>
                          {ind.display}
                        </span>
                        <span className="text-[10px] text-slate-400">{ind.note}</span>
                      </div>
                      <p className="text-xs text-slate-500 dark:text-slate-400 mt-1.5">
                        <span className="font-medium text-slate-600 dark:text-slate-300">金融含义：</span>
                        {INDICATOR_MEANING[ind.key] || ind.reason}
                      </p>
                      <p className="text-xs text-slate-500 dark:text-slate-400 mt-1">
                        <span className="font-medium text-slate-600 dark:text-slate-300">数值解释：</span>
                        {buildIndicatorAnalysis(ind, klineStats)}
                      </p>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
