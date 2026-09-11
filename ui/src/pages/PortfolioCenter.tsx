import { useEffect, useMemo, useState } from 'react'
import { RefreshCw, PieChart, History, Wand2, Target } from 'lucide-react'
import {
  runOptimize,
  getOptimizeRecords,
  METHOD_LABELS,
  METHOD_DESC,
  pct,
  type OptimizeMethod,
  type OptimizeResult,
  type OptimizeRecord,
  type OptimizeWeightItem,
} from '../services/portfolioOptimizer'
import { useToastStore } from '../store/toastStore'

const METHODS: OptimizeMethod[] = ['mvo', 'risk_parity', 'risk_budget']

const METHOD_COLORS: Record<OptimizeMethod, string> = {
  mvo: 'bg-purple-500',
  risk_parity: 'bg-blue-500',
  risk_budget: 'bg-emerald-500',
}

// 单一色系调色板（按权重条渲染）
const PALETTE = ['#6366F1', '#3B82F6', '#10B981', '#F59E0B', '#EC4899', '#14B8A6', '#8B5CF6', '#F43F5E', '#84CC16', '#06B6D4']

function Donut({ items, total }: { items: OptimizeWeightItem[]; total: number }) {
  const segs = items
    .filter((i) => i.weight > 0.0001)
    .map((i, idx) => {
      const pctShare = (i.weight / total) * 100
      return `${PALETTE[idx % PALETTE.length]} ${pctShare}%,`;
    })
    .join(' ')
  const clean = segs.replace(/,\s*$/, '')
  const style = items.some((i) => i.weight > 0.0001)
    ? { background: `conic-gradient(${clean})` }
    : { background: '#e2e8f0' }
  return (
    <div className="relative w-32 h-32 rounded-full shrink-0" style={style}>
      <div className="absolute inset-2 rounded-full bg-white dark:bg-slate-800 flex items-center justify-center">
        <div className="text-center">
          <div className="text-base font-bold text-slate-800 dark:text-white">{pct(total, 0)}</div>
          <div className="text-[9px] text-slate-400">目标权益配置</div>
        </div>
      </div>
    </div>
  )
}

export default function PortfolioCenter() {
  const toast = useToastStore((s) => s.success)
  const [method, setMethod] = useState<OptimizeMethod>('risk_parity')
  const [totalWeight, setTotalWeight] = useState(90)
  const [maxSingle, setMaxSingle] = useState(30)
  const [days, setDays] = useState(90)
  const [loading, setLoading] = useState(false)
  const [result, setResult] = useState<OptimizeResult | null>(null)
  const [error, setError] = useState('')
  const [records, setRecords] = useState<OptimizeRecord[]>([])
  const [loadHist, setLoadHist] = useState(false)

  const loadHistory = async () => {
    setLoadHist(true)
    const recs = await getOptimizeRecords(10)
    setRecords(recs)
    setLoadHist(false)
  }
  useEffect(() => {
    loadHistory()
  }, [])

  const handleRun = async () => {
    setLoading(true)
    setError('')
    const res = await runOptimize({
      method,
      symbols: undefined,
      totalWeight: totalWeight / 100,
      maxSingle: maxSingle / 100,
      days,
    })
    setLoading(false)
    if (!res) {
      setError('优化失败：请检查当前持仓标的是否有足够K线历史（至少2只）')
    } else if (res.code !== 'ok') {
      setError(res.message || '优化未执行')
    } else {
      setResult(res)
      toast(methodName(method) + ' 优化完成')
      loadHistory()
    }
  }

  const sortedWeights = useMemo(
    () => (result ? [...result.weights].sort((a, b) => b.weight - a.weight) : []),
    [result],
  )

  return (
    <div className="p-6 space-y-5">
      <div className="flex items-center gap-2">
        <PieChart className="w-5 h-5 text-indigo-500" />
        <h1 className="text-lg font-semibold text-slate-800 dark:text-slate-100">投资组合中心</h1>
        <span className="text-xs text-slate-400 ml-1">基于真实持仓/日K协方差的最优配置（仅供研究，不影响投资决策）</span>
      </div>

      {/* 参数控制 */}
      <div className="bg-white dark:bg-slate-800 rounded-xl border border-slate-200 dark:border-slate-700 p-4 space-y-4">
        {/* 算法选择 */}
        <div>
          <div className="text-xs font-medium text-slate-500 dark:text-slate-400 mb-2">优化算法</div>
          <div className="grid grid-cols-3 gap-2">
            {METHODS.map((m) => (
              <button
                key={m}
                onClick={() => setMethod(m)}
                className={`rounded-lg border p-3 text-left transition ${
                  method === m
                    ? 'border-indigo-400 bg-indigo-50 dark:bg-indigo-900/30 ring-1 ring-indigo-400'
                    : 'border-slate-200 dark:border-slate-700 hover:border-indigo-200'
                }`}
              >
                <div className={`text-sm font-medium text-slate-800 dark:text-slate-100`}>{METHOD_LABELS[m]}</div>
                <div className="text-xs text-slate-400 mt-0.5 leading-snug">{METHOD_DESC[m]}</div>
              </button>
            ))}
          </div>
        </div>

        {/* 参数 */}
        <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
          <ParamSlider label="权益配置比例" value={totalWeight} min={50} max={100} suffix="%" onChange={setTotalWeight} />
          <ParamSlider label="单券上限" value={maxSingle} min={5} max={50} suffix="%" onChange={setMaxSingle} />
          <div>
            <div className="text-xs font-medium text-slate-500 mb-1">收益窗口(日)</div>
            <select
              value={days}
              onChange={(e) => setDays(Number(e.target.value))}
              className="w-full rounded-lg border border-slate-200 dark:border-slate-700 bg-transparent px-3 py-2 text-sm dark:text-slate-100"
            >
              <option value={60}>60 日</option>
              <option value={90}>90 日</option>
              <option value={120}>120 日</option>
              <option value={250}>250 日</option>
            </select>
          </div>
        </div>

        <div className="flex items-center gap-3">
          <button
            onClick={handleRun}
            disabled={loading}
            className="inline-flex items-center gap-2 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-60"
          >
            {loading ? <RefreshCw className="w-4 h-4 animate-spin" /> : <Wand2 className="w-4 h-4" />}
            {loading ? '优化中…' : '运行组合优化'}
          </button>
          <span className="text-xs text-slate-400">
            {result ? (result.isCurrentHold ? '基于当前持仓' : '基于指定标的') : '默认基于当前持仓'}
          </span>
        </div>
        {error && <div className="text-sm text-red-500">{error}</div>}
      </div>

      {/* 结果 */}
      {result && (
        <div className="grid grid-cols-1 lg:grid-cols-5 gap-4">
          {/* 指标卡 */}
          <div className="lg:col-span-2 grid grid-cols-2 gap-3 content-start">
            <Metric label="年化波动率" value={pct(result.volatility)} sub="风险" />
            <Metric label="年化预期收益" value={pct(result.annualReturn)} sub="收益" />
            <Metric label="夏普比率" value={result.sharpe != null ? result.sharpe.toFixed(2) : '-'} sub="风险调整收益" />
            <Metric label="目标权益合计" value={pct(result.totalWeight)} sub="其余为现金" />
          </div>

          {/* 组合配置图 */}
          <div className="lg:col-span-3 bg-white dark:bg-slate-800 rounded-xl border border-slate-200 dark:border-slate-700 p-4">
            <div className="text-sm font-medium text-slate-700 dark:text-slate-200 mb-3 flex items-center gap-2">
              <Target className="w-4 h-4 text-indigo-500" /> 目标配置
            </div>
            <div className="flex gap-4">
              <Donut items={result.weights} total={result.totalWeight} />
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 text-xs text-slate-400 mb-1 px-1">
                  <span className="w-20 shrink-0">股票代码</span>
                  <span className="flex-1">股票名称</span>
                  <span className="w-16 text-right">比例</span>
                </div>
                <div className="space-y-1.5">
                  {sortedWeights.map((w, idx) => (
                    <div key={w.code} className="flex items-center gap-2 text-sm px-1">
                      <span className="w-3 h-3 rounded-sm shrink-0" style={{ background: PALETTE[idx % PALETTE.length] }} />
                      <span className="w-16 text-xs text-slate-400 truncate shrink-0">{w.code}</span>
                      <span className="flex-1 truncate text-slate-700 dark:text-slate-200">{w.name}</span>
                      <span className="w-16 text-right font-medium text-slate-700 dark:text-slate-200 shrink-0">{pct(w.weight, 1)}</span>
                    </div>
                  ))}
                </div>
              </div>
            </div>
            <div className="mt-3 text-xs text-slate-400">
              风险贡献占比：{sortedWeights.map((w) => `${w.name} ${pct(w.riskPct, 1)}`).join('、')}
            </div>
          </div>
        </div>
      )}

      {/* 历史记录 */}
      <div className="bg-white dark:bg-slate-800 rounded-xl border border-slate-200 dark:border-slate-700 p-4">
        <div className="text-sm font-medium text-slate-700 dark:text-slate-200 mb-3 flex items-center gap-2">
          <History className="w-4 h-4 text-indigo-500" /> 优化历史
          {loadHist && <RefreshCw className="w-3 h-3 animate-spin text-slate-400" />}
        </div>
        {records.length === 0 ? (
          <div className="text-sm text-slate-400">暂无优化记录，运行一次组合优化后展示</div>
        ) : (
          <div className="space-y-2">
            {records.map((r) => (
              <div key={r.id} className="flex items-center gap-3 text-sm border-b border-slate-100 dark:border-slate-700 pb-2 last:border-0">
                <span className={`px-2 py-0.5 rounded ${METHOD_COLORS[r.method] ?? 'bg-slate-400'} text-white text-xs`}>{METHOD_LABELS[r.method] ?? r.method}</span>
                <span className="text-slate-600 dark:text-slate-300">{r.remark || ''}</span>
                <span className="text-xs text-slate-400 ml-auto">
                  波动 {pct(r.volatility)} · 夏普 {r.sharpe ? r.sharpe.toFixed(2) : '-'}
                </span>
                <span className="text-xs text-slate-400">{r.created_at}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

function ParamSlider({
  label,
  value,
  min,
  max,
  suffix,
  onChange,
}: {
  label: string
  value: number
  min: number
  max: number
  suffix: string
  onChange: (v: number) => void
}) {
  return (
    <div>
      <div className="flex justify-between text-xs font-medium text-slate-500 mb-1">
        <span>{label}</span>
        <span>{value}{suffix}</span>
      </div>
      <input
        type="range"
        min={min}
        max={max}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="w-full accent-indigo-500"
      />
    </div>
  )
}

function Metric({ label, value, sub }: { label: string; value: string; sub: string }) {
  return (
    <div className="rounded-xl border border-slate-200 dark:border-slate-700 p-3">
      <div className="text-xs text-slate-400">{label}</div>
      <div className="text-lg font-semibold text-slate-800 dark:text-slate-100 mt-0.5">{value}</div>
      <div className="text-[11px] text-slate-400">{sub}</div>
    </div>
  )
}

function methodName(m: OptimizeMethod): string {
  return METHOD_LABELS[m] ?? m
}