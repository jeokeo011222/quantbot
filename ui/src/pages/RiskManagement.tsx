import { useState, useCallback, useEffect } from 'react'
import {
  Gauge,
  Activity,
  CheckCircle,
  AlertTriangle,
  Play,
  History,
} from 'lucide-react'
import {
  runRiskReport,
  getRiskReports,
  getRiskGuardStatus,
  getLatestDailyReview,
  type RiskReport,
  type RiskGuardStatus,
} from '../services/riskManagement'
import { useToastStore } from '../store/toastStore'

// 风险状态 → 展示样式（与风控师 severity 分级一致）
const STATUS_META: Record<string, { label: string; color: string; bg: string; border: string }> = {
  NORMAL: { label: '正常', color: 'text-green-600 dark:text-green-400', bg: 'bg-green-50 dark:bg-green-900/20', border: 'border-green-200 dark:border-green-800' },
  WATCH: { label: '关注', color: 'text-yellow-600 dark:text-yellow-400', bg: 'bg-yellow-50 dark:bg-yellow-900/20', border: 'border-yellow-200 dark:border-yellow-800' },
  WARNING: { label: '警告', color: 'text-orange-600 dark:text-orange-400', bg: 'bg-orange-50 dark:bg-orange-900/20', border: 'border-orange-200 dark:border-orange-800' },
  CRITICAL: { label: '严重', color: 'text-red-600 dark:text-red-400', bg: 'bg-red-50 dark:bg-red-900/20', border: 'border-red-200 dark:border-red-800' },
}

// 压力测试情景中文映射（兼容旧数据库英文标识与后端新输出的中文）
const SCENARIO_ZH: Record<string, string> = {
  market_crash: '市场暴跌',
  volatility_spike: '波动率飙升',
  rate_shock: '利率冲击',
  liquidity_shock: '流动性枯竭',
  sector_crash: '板块崩盘',
  correlation_breakdown: '相关性破裂',
}

// 指标卡片元数据
const METRIC_CARDS = [
  { key: 'var_95', label: 'VaR 95%', unit: '%', fmt: (v: number) => (v * 100).toFixed(2), tip: '历史法在险价值，95%置信度下组合单日最大潜在回撤' },
  { key: 'var_99', label: 'VaR 99%', unit: '%', fmt: (v: number) => (v * 100).toFixed(2), tip: '历史法在险价值，99%置信度下组合单日最大潜在回撤' },
  { key: 'cvar_95', label: 'CVaR 95%', unit: '%', fmt: (v: number) => (v * 100).toFixed(2), tip: '条件在险价值，尾部损失均值；风控师约束 ≤5%' },
  { key: 'volatility', label: '年化波动率', unit: '%', fmt: (v: number) => (v * 100).toFixed(2), tip: '组合日收益年化标准差，衡量价格波动程度' },
  { key: 'max_drawdown', label: '最大回撤', unit: '%', fmt: (v: number) => (v * 100).toFixed(2), tip: '历史峰值到谷底的最大跌幅；风控师硬限制 ≤20%' },
  { key: 'beta', label: 'Beta', unit: '', fmt: (v: number) => v.toFixed(2), tip: '组合相对上证指数的系统性风险敏感度' },
  { key: 'correlation', label: '相关性', unit: '', fmt: (v: number) => v.toFixed(2), tip: '组合收益与基准收益的相关性' },
  { key: 'concentration', label: '集中度(HHI)', unit: '', fmt: (v: number) => v.toFixed(3), tip: '组合集中度，≤0.5 视为分散良好' },
  { key: 'liquidity', label: '流动性', unit: '', fmt: (v: number) => v.toFixed(2), tip: '流动性得分，越低流动性风险越小' },
]

// 复盘字段中文名映射（供盘后复盘板块展示）
const REVIEW_FIELDS: { key: string; label: string; fmt: (v: unknown) => string }[] = [
  { key: 'review_date', label: '复盘日期', fmt: (v) => String(v) },
  { key: 'market_state', label: '市场状态', fmt: (v) => String(v) },
  { key: 'market_confidence', label: '市场置信度', fmt: (v) => `${(Number(v) * 100).toFixed(1)}%` },
  { key: 'positions_count', label: '持仓数', fmt: (v) => String(v) },
  { key: 'total_assets', label: '总资产', fmt: fmtMoney },
  { key: 'cash', label: '现金', fmt: fmtMoney },
  { key: 'market_value', label: '市值', fmt: fmtMoney },
  { key: 'daily_pnl', label: '当日盈亏', fmt: fmtMoney },
  { key: 'daily_return', label: '当日收益率', fmt: fmtPct },
  { key: 'total_return', label: '累计收益', fmt: fmtPct },
  { key: 'key_events', label: '关键事件', fmt: fmtJsonList },
  { key: 'decisions', label: '决策', fmt: fmtJsonList },
  { key: 'agent_summaries', label: '智能体复盘汇总', fmt: fmtJsonList },
  { key: 'summary', label: '总结', fmt: (v) => String(v) },
  { key: 'recommendations', label: '建议', fmt: (v) => String(v) },
]

// 金额（元）
function fmtMoney(v: unknown): string {
  const n = Number(v)
  if (!Number.isFinite(n)) return String(v)
  return `¥${n.toLocaleString('zh-CN', { maximumFractionDigits: 2 })}`
}
// 百分数值：组合引擎的 daily_return / total_return 已为百分比例（-0.406 即 -0.41%）
function fmtPct(v: unknown): string {
  const n = Number(v)
  if (!Number.isFinite(n)) return String(v)
  return `${n.toFixed(2)}%`
}
// JSON blob 字段（关键事件/决策/复盘汇总）压缩为可读文本，避免大段原始 JSON / [object Object]
function fmtJsonList(v: unknown): string {
  if (v == null) return ''
  const s = typeof v === 'string' ? v.trim() : JSON.stringify(v)
  if (!s || s === '{}' || s === '[]') return ''
  try {
    const obj = JSON.parse(s)
    if (Array.isArray(obj)) {
      return obj
        .map((it) => {
          if (it && typeof it === 'object') {
            const parts = [it.date || it.timestamp || '', it.title || it.decision || it.reason || it.name || '']
            return parts.filter(Boolean).join(' ')
          }
          return String(it)
        })
        .filter(Boolean)
        .join('；')
    }
    if (typeof obj === 'object') {
      return Object.entries(obj as Record<string, unknown>)
        .map(([k, val]) => `${k}: ${Array.isArray(val) ? val.length : JSON.stringify(val)}`)
        .join('；')
    }
    return JSON.stringify(obj)
  } catch {
    return s.length > 200 ? `${s.slice(0, 200)}…` : s
  }
}

function SectionCard({ title, desc, children }: { title: string; desc?: string; children: React.ReactNode }) {
  return (
    <div className="bg-white dark:bg-slate-800 border border-slate-200 dark:border-slate-700 rounded-xl p-5">
      <div className="flex items-center gap-2 mb-4">
        <h2 className="font-semibold text-slate-800 dark:text-slate-100">{title}</h2>
        {desc && <span className="text-xs text-slate-400 dark:text-slate-500">{desc}</span>}
      </div>
      {children}
    </div>
  )
}

function Th({ children }: { children?: React.ReactNode }) {
  return <th className="px-3 py-2 text-left text-xs font-medium text-slate-400 dark:text-slate-500">{children}</th>
}
function Td({ children, className = '' }: { children?: React.ReactNode; className?: string }) {
  return <td className={`px-3 py-2 text-sm text-slate-600 dark:text-slate-300 ${className}`}>{children}</td>
}

export default function RiskManagement() {
  const [report, setReport] = useState<RiskReport | null>(null)
  const [history, setHistory] = useState<RiskReport[]>([])
  const [guard, setGuard] = useState<RiskGuardStatus | null>(null)
  const [review, setReview] = useState<Record<string, unknown> | null>(null)
  const [loading, setLoading] = useState(false)
  const toast = useToastStore((s) => s.error)

  const loadAll = useCallback(async () => {
    const [h, g, r] = await Promise.all([getRiskReports(7), getRiskGuardStatus(), getLatestDailyReview()])
    setHistory(h)
    setGuard(g)
    // 后端 GetLatestDailyReview 返回 { review: DailyReview }，解包后才是复盘实体
    setReview((r?.review as Record<string, unknown>) ?? null)
    if (h.length > 0) setReport((prev) => prev ?? h[0])
  }, [])

  useEffect(() => {
    loadAll()
  }, [loadAll])

  const runNow = async () => {
    setLoading(true)
    try {
      const rep = await runRiskReport()
      if (rep) {
        setReport(rep)
        await loadAll()
        toast('风险报告已生成', '已落库，可在下方历史报告中回看')
      } else {
        toast('生成失败', '请检查后端是否就绪')
      }
    } finally {
      setLoading(false)
    }
  }

  const overall = report ? Math.round((report.overall_score ?? 0) * 100) : null
  const statusMeta = report ? STATUS_META[report.status] ?? STATUS_META.NORMAL : null

  return (
    <div className="space-y-6">
      {/* 页头 */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-slate-800 dark:text-slate-100">风险管理</h1>
          <p className="text-sm text-slate-500 dark:text-slate-400 mt-1">
            与风控师同一套引擎（RiskEngine + PolicyEngine），指标口径完全一致
          </p>
        </div>
        <button
          onClick={runNow}
          disabled={loading}
          className="flex items-center gap-2 px-4 py-2 rounded-lg bg-blue-600 hover:bg-blue-700 disabled:opacity-50 text-white text-sm font-medium transition-colors"
        >
          <Play className="w-4 h-4" />
          {loading ? '计算中…' : '生成最新风险报告'}
        </button>
      </div>

      {/* 模块1 组合风险总览 */}
      <SectionCard title="组合风险总览" desc={`报告日 ${report?.report_date ?? '-'} · 持仓 ${report?.position_count ?? 0} 只 · 总暴露 ${((report?.total_exposure ?? 0) * 100).toFixed(1)}%`}>
        {!report ? (
          <p className="text-sm text-slate-400 py-8 text-center">暂无报告，点击右上角「生成最新风险报告」</p>
        ) : (
          <div className="space-y-4">
            <div className="flex items-center gap-4 bg-slate-50 dark:bg-slate-900/40 rounded-xl p-4">
              <div className={`flex-1 rounded-xl border p-4 ${statusMeta!.border} ${statusMeta!.bg}`}>
                <div className="text-xs text-slate-500 dark:text-slate-400">综合风险评分</div>
                <div className={`text-3xl font-bold ${statusMeta!.color}`}>{overall} 分</div>
                <div className={`inline-block mt-1 px-2 py-0.5 rounded text-xs font-semibold ${statusMeta!.color}`}>状态：{statusMeta!.label}</div>
              </div>
              <div className="flex-1 text-xs text-slate-500 dark:text-slate-400">
                <p className="mb-1">综合评分由 VaR / CVaR / 波动率 / 最大回撤等加权合成，并按阈值划分风险等级，与风控师盘后复盘判定逻辑一致。</p>
                <p>指标口径：VaR/CVaR 历史法 · 波动率年化 · 回撤峰值法 · Beta/相关性对标上证指数。</p>
              </div>
            </div>
            <div className="grid grid-cols-3 md:grid-cols-5 gap-3">
              {METRIC_CARDS.map((m) => (
                <div key={m.key} title={m.tip} className="bg-slate-50 dark:bg-slate-900/40 rounded-lg p-3">
                  <div className="text-xs text-slate-400 dark:text-slate-500">{m.label}</div>
                  <div className="text-lg font-semibold text-slate-800 dark:text-slate-100">
                    {m.fmt((report as unknown as Record<string, number>)[m.key] ?? 0)}
                    {m.unit}
                  </div>
                </div>
              ))}
            </div>
            {report.violations && report.violations.length > 0 && (
              <div className="rounded-lg border border-red-200 dark:border-red-800 bg-red-50 dark:bg-red-900/20 p-3 text-sm">
                <div className="flex items-center gap-1.5 text-red-600 dark:text-red-400 font-medium mb-1">
                  <AlertTriangle className="w-4 h-4" /> 合规告警
                </div>
                <ul className="list-disc pl-5 text-red-600 dark:text-red-400">
                  {report.violations.map((v, i) => (
                    <li key={i}>{v}</li>
                  ))}
                </ul>
              </div>
            )}
          </div>
        )}
      </SectionCard>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {/* 模块2 压力测试矩阵 */}
        <SectionCard title="压力测试矩阵" desc="预估损失 ≤15% 视为通过">
          {!report?.stress_tests?.length ? (
            <p className="text-sm text-slate-400 py-6 text-center">无压力测试数据</p>
          ) : (
            <table className="w-full border-collapse">
              <thead>
                <tr className="border-b border-slate-200 dark:border-slate-700">
                  <Th>情景</Th>
                  <Th>预估损失</Th>
                  <Th>判断</Th>
                </tr>
              </thead>
              <tbody>
                {report.stress_tests.map((s, i) => (
                  <tr key={i} className="border-b border-slate-100 dark:border-slate-700/50">
                    <Td className="font-medium">{SCENARIO_ZH[s.scenario] ?? s.scenario}</Td>
                    <Td className={s.estimated_loss > 0.15 ? 'text-red-600 dark:text-red-400' : ''}>
                      {(s.estimated_loss * 100).toFixed(2)}%
                    </Td>
                    <Td>
                      <span
                        className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium ${
                          s.passed ? 'bg-green-50 text-green-600 dark:bg-green-900/20 dark:text-green-400' : 'bg-red-50 text-red-600 dark:bg-red-900/20 dark:text-red-400'
                        }`}
                      >
                        {s.passed ? <CheckCircle className="w-3 h-3" /> : <AlertTriangle className="w-3 h-3" />}
                        {s.passed ? '通过' : '未通过'}
                      </span>
                    </Td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </SectionCard>

        {/* 模块3 合规约束校验 */}
        <SectionCard title="合规约束校验" desc="PolicyEngine 硬限制">
          {!report?.limits?.length ? (
            <p className="text-sm text-slate-400 py-6 text-center">无合规校验数据</p>
          ) : (
            <table className="w-full border-collapse">
              <thead>
                <tr className="border-b border-slate-200 dark:border-slate-700">
                  <Th>约束项</Th>
                  <Th>当前值</Th>
                  <Th>上限</Th>
                  <Th>判断</Th>
                </tr>
              </thead>
              <tbody>
                {report.limits.map((c, i) => (
                  <tr key={i} className="border-b border-slate-100 dark:border-slate-700/50">
                    <Td className="font-medium">{c.name}</Td>
                    <Td>{(c.actual * 100).toFixed(1)}%</Td>
                    <Td>{c.limit ? `${(c.limit * 100).toFixed(1)}%` : '—'}</Td>
                    <Td>
                      <span
                        className={`inline-flex items-center gap-1 px-2 py-0.5 rounded text-xs font-medium ${
                          c.passed ? 'bg-green-50 text-green-600 dark:bg-green-900/20 dark:text-green-400' : 'bg-red-50 text-red-600 dark:bg-red-900/20 dark:text-red-400'
                        }`}
                      >
                        {c.passed ? <CheckCircle className="w-3 h-3" /> : <AlertTriangle className="w-3 h-3" />}
                        {c.passed ? '通过' : '超限'}
                      </span>
                    </Td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </SectionCard>
      </div>

      {/* 模块4 盘中熔断状态 */}
      <SectionCard title="盘中熔断状态" desc="实时守护开关 · 与风控师同一 PolicyEngine">
        <div className="flex items-center gap-3 mb-4 p-3 rounded-xl bg-slate-50 dark:bg-slate-900/40">
          <Gauge className="w-5 h-5 text-slate-400" />
          <div className="text-sm">
            <span className="font-medium text-slate-700 dark:text-slate-200">交易运行：</span>
            <span className={guard?.trading ? 'text-green-600 dark:text-green-400' : 'text-slate-500 dark:text-slate-400'}>
              {guard?.trading ? '进行中' : '未运行/暂停'}
            </span>
            <span className="ml-4 font-medium text-slate-700 dark:text-slate-200">紧急熔断：</span>
            <span className={guard?.emergency_stopped ? 'text-red-600 dark:text-red-400 font-semibold' : 'text-green-600 dark:text-green-400'}>
              {guard?.emergency_stopped ? '已触发' : '未触发'}
            </span>
            {guard?.timestamp && <span className="ml-4 text-xs text-slate-400">更新于 {guard.timestamp}</span>}
          </div>
        </div>
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
          {(guard?.items ?? []).map((it) => (
            <div
              key={it.key}
              className={`rounded-lg border p-3 ${
                it.status === 'tripped'
                  ? 'border-red-300 dark:border-red-800 bg-red-50 dark:bg-red-900/20'
                  : it.status === 'ok'
                  ? 'border-green-200 dark:border-green-800 bg-green-50 dark:bg-green-900/20'
                  : 'border-slate-200 dark:border-slate-700 bg-slate-50 dark:bg-slate-900/40'
              }`}
            >
              <div className="flex items-center justify-between">
                <span className="text-xs font-medium text-slate-600 dark:text-slate-300">{it.label}</span>
                <span
                  className={`w-2 h-2 rounded-full ${
                    it.status === 'tripped' ? 'bg-red-500' : it.status === 'ok' ? 'bg-green-500' : 'bg-slate-400'
                  }`}
                />
              </div>
              <div className="mt-1 text-xs text-slate-500 dark:text-slate-400">{it.message}</div>
            </div>
          ))}
        </div>
      </SectionCard>

      {/* 模块5 盘后复盘 */}
      <SectionCard title="盘后复盘" desc="当日复盘 + 历史风险报告回看">
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
          <div className="rounded-xl bg-slate-50 dark:bg-slate-900/40 p-4">
            <div className="flex items-center gap-1.5 text-sm font-medium text-slate-700 dark:text-slate-200 mb-2">
              <Activity className="w-4 h-4" /> 最新每日复盘
            </div>
            {!review ? (
              <p className="text-sm text-slate-400 py-4 text-center">暂无复盘记录</p>
            ) : (
              <div className="text-sm text-slate-600 dark:text-slate-300 space-y-1">
                {REVIEW_FIELDS.map(({ key, label, fmt }) => {
                  if (!(key in review)) return null
                  const txt = fmt(review[key])
                  return (
                    <div key={key} className="flex gap-2">
                      <span className="text-slate-400 dark:text-slate-500 shrink-0">{label}:</span>
                      <span className="break-all whitespace-pre-wrap">
                        {txt === '' ? <span className="text-slate-300 dark:text-slate-600">-</span> : txt}
                      </span>
                    </div>
                  )
                })}
              </div>
            )}
          </div>
          <div>
            <div className="flex items-center gap-1.5 text-sm font-medium text-slate-700 dark:text-slate-200 mb-2">
              <History className="w-4 h-4" /> 历史风险报告
            </div>
            {history.length === 0 ? (
              <p className="text-sm text-slate-400 py-4 text-center">暂无历史报告</p>
            ) : (
              <table className="w-full border-collapse">
                <thead>
                  <tr className="border-b border-slate-200 dark:border-slate-700">
                    <Th>日期</Th>
                    <Th>来源</Th>
                    <Th>评分</Th>
                    <Th>状态</Th>
                  </tr>
                </thead>
                <tbody>
                  {history.map((r, i) => (
                    <tr
                      key={i}
                      className="border-b border-slate-100 dark:border-slate-700/50 cursor-pointer hover:bg-slate-50 dark:hover:bg-slate-800/50"
                      onClick={() => setReport(r)}
                    >
                      <Td>{r.report_date}</Td>
                      <Td>{r.source}</Td>
                      <Td>{((r.overall_score ?? 0) * 100).toFixed(0)} 分</Td>
                      <Td>
                        <span className={`px-2 py-0.5 rounded text-xs font-medium ${(STATUS_META[r.status] ?? STATUS_META.NORMAL).color}`}>
                          {(STATUS_META[r.status] ?? STATUS_META.NORMAL).label}
                        </span>
                      </Td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </div>
      </SectionCard>
    </div>
  )
}