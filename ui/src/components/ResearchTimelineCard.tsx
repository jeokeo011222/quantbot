import { useEffect, useState } from 'react'
import { History, TrendingUp, TrendingDown, Minus, CircleDot } from 'lucide-react'
import { GetResearchTimeline } from '../../wailsjs/go/main/App'

// 单日研究记忆（对应后端 cio.ResearchDay）
interface ResearchDay {
  trade_date: string
  market_tag?: string
  adjusted_total_score?: number
  position_rate?: number
  dim_scores?: Record<string, number>
  claim?: {
    claim_date: string
    direction: string
    confidence: number
    statement?: string
    verified: boolean
    match: boolean
    actual_return: number
  }
}

interface TimelineSummary {
  days: number
  verified: number
  agreed: number
  direction_hit_rate_pct: number
}

function DirectionIcon({ direction }: { direction: string }) {
  if (direction === 'bullish') return <TrendingUp className="w-4 h-4 text-green-500" />
  if (direction === 'bearish') return <TrendingDown className="w-4 h-4 text-red-500" />
  return <Minus className="w-4 h-4 text-slate-400" />
}

// 修正总分(0~100)的中文解读：数值越高，当天的六维判势越偏多。
function scoreMeaning(score: number): { label: string; cls: string } {
  if (!Number.isFinite(score)) return { label: '数据异常', cls: 'text-slate-400' }
  if (score >= 60) return { label: '偏强', cls: 'text-green-600 dark:text-green-400' }
  if (score >= 40) return { label: '震荡', cls: 'text-slate-500 dark:text-slate-400' }
  return { label: '偏弱', cls: 'text-red-500 dark:text-red-400' }
}

// 研究记忆时间轴：历史预期 → 次日验证（借鉴 easy-stock 研究飞轮，本机保留判断历史）
export default function ResearchTimelineCard() {
  const [timeline, setTimeline] = useState<ResearchDay[]>([])
  const [summary, setSummary] = useState<TimelineSummary>({ days: 0, verified: 0, agreed: 0, direction_hit_rate_pct: 0 })
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    let cancelled = false
    GetResearchTimeline(30)
      .then((res: any) => {
        if (cancelled) return
        if (res?.timeline) setTimeline(res.timeline as ResearchDay[])
        if (res?.summary) setSummary(res.summary as TimelineSummary)
      })
      .catch((err) => console.error('加载研究记忆时间轴失败:', err))
      .finally(() => !cancelled && setLoaded(true))
    return () => {
      cancelled = true
    }
  }, [])

  if (!loaded) {
    return null // 数据未就绪时静默，不打扰复盘页
  }
  if (timeline.length === 0) {
    return null // 无历史数据时不展示，避免空卡片
  }

  const hitRate = Number.isFinite(summary.direction_hit_rate_pct) ? summary.direction_hit_rate_pct : 0

  return (
    <div className="card p-5 mb-4">
      <div className="flex items-center gap-2 mb-1">
        <History className="w-4 h-4 text-brand-500" />
        <h2 className="text-sm font-semibold text-slate-700 dark:text-slate-300">研究记忆 · 历史预期验证</h2>
      </div>
      <p className="text-xs text-slate-500 dark:text-slate-400 mb-1">
        每日判断 &laquo;历史预期 → 次日验证&raquo;闭环（已验证 {summary.verified} 天 / 命中 {summary.agreed} 天，方向命中率 {hitRate.toFixed(1)}%）
      </p>
      <p className="text-xs text-slate-400 dark:text-slate-500 mb-4">
        "分"为当日市场六维判势的修正总分（0~100，随市场矛盾维数折价）：分数越高越偏多，≥60 偏强、40~59 震荡、&lt;40 偏弱。
      </p>
      <div className="space-y-2">
        {timeline.slice().reverse().map((d) => (
          <div key={d.trade_date} className="grid grid-cols-4 items-center gap-3 p-2.5 rounded-lg bg-slate-50 dark:bg-slate-900/40 border border-slate-100 dark:border-slate-800">
            <div className="flex items-center gap-2 min-w-0">
              <CircleDot className="w-3.5 h-3.5 shrink-0 text-slate-300 dark:text-slate-600" />
              <span className="text-xs font-medium text-slate-600 dark:text-slate-300 truncate">{d.trade_date}</span>
            </div>
            <div className="min-w-0">
              <span className="badge bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300">{d.market_tag || '—'}</span>
            </div>
            <div className="min-w-0 text-xs text-slate-500">
              {typeof d.adjusted_total_score === 'number' ? (
                <span title={`修正总分(0~100)，越高越偏多：${scoreMeaning(d.adjusted_total_score).label}`}>
                  {d.adjusted_total_score.toFixed(1)}分{' '}
                  <span className={scoreMeaning(d.adjusted_total_score).cls}>{scoreMeaning(d.adjusted_total_score).label}</span>
                </span>
              ) : (
                '—'
              )}
            </div>
            <div className="min-w-0">
              {d.claim ? (
                <span className="text-xs text-slate-600 dark:text-slate-300 flex items-center gap-1.5">
                  <DirectionIcon direction={d.claim.direction} />
                  {d.claim.direction === 'bullish' ? '看多' : d.claim.direction === 'bearish' ? '看空' : '中性'}
                  <span className="text-slate-400">信心{d.claim.confidence.toFixed(0)}</span>
                  {d.claim.verified ? (
                    <span className={`badge ${d.claim.match ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400' : 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'}`}>
                      {d.claim.match ? `命中${d.claim.actual_return >= 0 ? '+' : ''}${d.claim.actual_return.toFixed(2)}%` : `未命中${d.claim.actual_return >= 0 ? '+' : ''}${d.claim.actual_return.toFixed(2)}%`}
                    </span>
                  ) : (
                    <span className="badge bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400">待验证</span>
                  )}
                </span>
              ) : (
                <span className="text-xs text-slate-400">无当日判断</span>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}