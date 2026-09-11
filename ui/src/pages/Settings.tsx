import { useState, useEffect, useRef } from 'react'
import { Settings as SettingsIcon, Cpu, Database, Zap, Key, Save, TestTube, FileText, Info, CheckCircle, RefreshCw, AlertTriangle, Wrench, Trash2, HardDrive, Download, HeartPulse, ArrowRightLeft, FolderOpen, Globe, Rocket, Loader2, HeartHandshake, BarChart3, Activity, Calendar } from 'lucide-react'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'
import { useI18nStore } from '../store/i18nStore'
import type { Language } from '../store/i18nStore'
import { useThemeStore } from '../store/themeStore'
import AuditLogPage from './AuditLog'
import AppLogo from '../components/AppLogo'
// 联系与捐赠二维码图片（与 ui/README.md Community 部分一致）
import wxPayImg from '../../image/WX收款.png'
import qqVipImg from '../../image/QQvip1群.jpg'
import qqFreeImg from '../../image/QQFree1群.jpg'
import wxBizImg from '../../image/WX商务.jpg'

type TabKey = 'general' | 'trading' | 'ai' | 'datasource' | 'audit' | 'maintenance' | 'health' | 'about' | 'contact'

// Wails 后端 API 访问助手
type AppMethod = (...args: unknown[]) => Promise<unknown> | undefined

function getApp(): Record<string, AppMethod> | null {
  try {
    const w = window as unknown as { go?: { main?: { App?: Record<string, AppMethod> } } }
    return w.go?.main?.App ?? null
  } catch {
    return null
  }
}

async function callApp<T>(method: string, ...args: unknown[]): Promise<T> {
  const app = getApp()
  if (!app || typeof app[method] !== 'function') {
    throw new Error(`Method ${method} not available`)
  }
  const result = await app[method]!(...args)
  return result as T
}

export default function Settings() {
  const t = useI18nStore((s) => s.t)
  const { language, setLanguage } = useI18nStore()
  const { theme, setTheme } = useThemeStore()

  const [activeTab, setActiveTab] = useState<TabKey>('general')
  const [saved, setSaved] = useState(false)

  // 通用设置
  const [market, setMarket] = useState('CN')
  const [tradingMode, setTradingMode] = useState('simulated')
  const [initialCapital, setInitialCapital] = useState(100000)
  const [activityRefreshMinutes, setActivityRefreshMinutes] = useState(5)

  // QMT (迅投 XtQuant) 实盘交易接口设置
  const [qmtEnabled, setQmtEnabled] = useState(false)
  const [qmtPath, setQmtPath] = useState('')
  const [qmtAccount, setQmtAccount] = useState('')
  const [qmtAccountType, setQmtAccountType] = useState('STOCK')
  const [qmtMiniQMT, setQmtMiniQMT] = useState(true)
  const [qmtStrategyName, setQmtStrategyName] = useState('QuantBot')
  const [qmtStrategyPath, setQmtStrategyPath] = useState('')
  const [qmtFolderStatus, setQmtFolderStatus] = useState<any>(null)
  const [qmtAutoExecution, setQmtAutoExecution] = useState(false)
  const [qmtTesting, setQmtTesting] = useState(false)
  const [qmtTestResult, setQmtTestResult] = useState<any>(null)

  // AI 设置
  const [aiProvider, setAiProvider] = useState('deepseek')
  const [aiModel, setAiModel] = useState('deepseek-chat')
  const [aiBaseURL, setAiBaseURL] = useState('https://api.deepseek.com')
  const [apiKey, setApiKey] = useState('')

  // AI 测试状态
  const [testingAI, setTestingAI] = useState(false)
  const [aiTestResult, setAiTestResult] = useState<any>(null)

  // 数据源设置
  const [dataProvider, setDataProvider] = useState('native_tdx')
  const [tdxPath, setTdxPath] = useState('D:\\tdx')
  const [mcpURL, setMcpURL] = useState('http://127.0.0.1:8765')
  const [mcpAPIKey, setMcpAPIKey] = useState('')

  // 市场六维判势数据源配置（持久化到 config.json，优先级越小越先尝试，高优先级失败自动回退）
  const defaultSixDimSource = () => ({
    northbound: { enabled: true, priority: 1 },
    margin: { enabled: true, priority: 2 },
    volume_expansion: { enabled: true, priority: 3 },
    ths_boards: { enabled: true, priority: 1 },
    limit_board: { enabled: true, priority: 1 },
    full_market_stats: { enabled: true, priority: 2 },
    overnight: { enabled: true, priority: 1 },
  })
  const [sixDimSource, setSixDimSource] = useState<any>(defaultSixDimSource())
  // 六维判势数据源卡片是否展开（选中卡片显示具体配置，对标腾讯财经卡片模式）
  const [sixDimOpen, setSixDimOpen] = useState(false)
  // 市场六维判势「数据测试」状态
  const [sixDimTesting, setSixDimTesting] = useState(false)
  const [sixDimTestResult, setSixDimTestResult] = useState<any>(null)
  // 市场六维判势「后台强制刷新」状态
  const [sixDimRefreshing, setSixDimRefreshing] = useState(false)
  const [sixDimRefreshResult, setSixDimRefreshResult] = useState<any>(null)
  const updateSixDimSource = (key: string, field: 'enabled' | 'priority', value: any) => {
    setSixDimSource((prev: any) => ({
      ...prev,
      [key]: { ...prev[key], [field]: value },
    }))
  }
  // 六维判势数据源展示信息（名称/说明/维度归属）
  const sixDimSourceMeta: Record<string, { name: string; desc: string }> = {
    northbound: { name: '北向资金（同花顺 hsgtApi）', desc: '北向实时净流入，资金结构维度最高优先级源' },
    margin: { name: '两融余额（东财数据中心）', desc: '融资余额连续增减天数，T-1 真实杠杆资金' },
    volume_expansion: { name: '成交额放量（DuckDB 兜底）', desc: '全市场成交额连续放量天数，资金结构兜底代理' },
    ths_boards: { name: '涨跌停/炸板池（同花顺官方）', desc: '官方涨停/跌停/炸板池+连板天梯，真实炸板率，需启用上方官方数据源并填 Key' },
    limit_board: { name: '涨跌停/炸板池（东财 push2ex）', desc: '实时涨停/跌停/炸板池，情绪维度高优先级源' },
    full_market_stats: { name: '全市场收盘统计（DuckDB 兜底）', desc: '全市场涨跌家数等收盘统计，情绪维度兜底' },
    overnight: { name: '隔夜外围（腾讯全球指数）', desc: '真实隔夜外围指数，4s超时+5min缓存，失败回退跳空代理' },
  }

  // 数据源测试状态
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<any>(null)

  // 数据源页内部页签：实时行情 / 辅助数据源
  const [dsTab, setDsTab] = useState<'realtime' | 'aux'>('realtime')

  // 同花顺官方数据源（fuyao.aicubes.cn）配置（默认展开同花顺卡片）
  const [thsOpen, setThsOpen] = useState(true)
  const [thsEnabled, setThsEnabled] = useState(false)
  const [thsApiKey, setThsApiKey] = useState('')
  const [thsBaseUrl, setThsBaseUrl] = useState('')
  const [thsSaved, setThsSaved] = useState(false)

  // 保存错误提示
  const [saveError, setSaveError] = useState<string>('')
  const [saveSuccess, setSaveSuccess] = useState(false)

  // Native TDX 状态
  const [nativeTDXConnected, setNativeTDXConnected] = useState(false)
  const [nativeTDXConnecting, setNativeTDXConnecting] = useState(false)
  const [nativeTDXResult, setNativeTDXResult] = useState<{ success: boolean; message: string } | null>(null)

  // 系统信息状态
  const [systemInfo, setSystemInfo] = useState<any>(null)

  // 软件更新状态
  const [updateInfo, setUpdateInfo] = useState<any>(null)
  const [updateState, setUpdateState] = useState<'idle' | 'checking' | 'available' | 'downloading' | 'ready' | 'error'>('idle')
  const [updateProgress, setUpdateProgress] = useState(0)
  const [updateError, setUpdateError] = useState<string>('')

  // 数据维护状态
  const [syncStatus, setSyncStatus] = useState<any>(null)
  const [syncTdxPath, setSyncTdxPath] = useState('')
  const [syncPolling, setSyncPolling] = useState(false)
  const [resetConfirm, setResetConfirm] = useState('')
  const [resetResult, setResetResult] = useState<any>(null)
  const [resetBusy, setResetBusy] = useState(false)
  const [cleanupResult, setCleanupResult] = useState<any>(null)
  const [cleanupBusy, setCleanupBusy] = useState(false)

  // 财务数据同步状态
  const [finSyncStatus, setFinSyncStatus] = useState<any>(null)
  const [finSyncPolling, setFinSyncPolling] = useState(false)
  // 同花顺复权因子导入 + 前复权查询
  const [adjBusy, setAdjBusy] = useState(false)
  const [adjStatus, setAdjStatus] = useState<any>(null)
  const [adjResult, setAdjResult] = useState<any>(null)
  const [adjErr, setAdjErr] = useState<string | null>(null)

  // 同花顺日K导入（stock.ohlc 底层 stock_daily）
  const [dailyKBusy, setDailyKBusy] = useState(false)
  const [dailyKStatus, setDailyKStatus] = useState<any>(null)
  const [dailyKResult, setDailyKResult] = useState<any>(null)
  const [dailyKErr, setDailyKErr] = useState<string | null>(null)

  // 同花顺交易日历同步
  const [calStatus, setCalStatus] = useState<any>(null)
  const [calResult, setCalResult] = useState<any>(null)
  const [calErr, setCalErr] = useState<string | null>(null)

  // 同花顺「财务数据」同步（见同花顺数据页签）

  // 系统健康状态
  const [health, setHealth] = useState<any>(null)
  const [healthLoading, setHealthLoading] = useState(false)
  const [healthError, setHealthError] = useState<string | null>(null)

  // 加载配置
  useEffect(() => {
    loadConfig()
  }, [])

  const loadConfig = async () => {
    try {
      const cfg = await callApp<any>('GetConfig')
      if (cfg) {
        setMarket(cfg.market || 'CN')
        // 兼容旧版 "paper" 模式，映射为 "simulated"
        const tm = cfg.trading_mode === 'paper' ? 'simulated' : (cfg.trading_mode || 'simulated')
        setTradingMode(tm)
        setInitialCapital(Math.min(cfg.initial_capital ?? 100000, 2000000))
        setActivityRefreshMinutes(cfg.activity_refresh_minutes || 5)
        setQmtEnabled(cfg.qmt_enabled || false)
        setQmtPath(cfg.qmt_path || '')
        setQmtAccount(cfg.qmt_account || '')
        setQmtAccountType(cfg.qmt_account_type || 'STOCK')
        setQmtMiniQMT(cfg.qmt_mini_qmt !== false)
        setQmtStrategyName(cfg.qmt_strategy_name || 'QuantBot')
        setQmtStrategyPath(cfg.qmt_strategy_path || '')
        setAiProvider(cfg.ai_provider || 'deepseek')
        setAiModel(cfg.ai_model || 'deepseek-chat')
        setAiBaseURL(cfg.ai_base_url || 'https://api.deepseek.com')
        // 回填已保存的 API Key（否则状态为空，再次保存会用空值覆盖已持久化的 key）
        setApiKey(cfg.ai_api_key || '')
        setDataProvider(cfg.data_provider || 'native_tdx')
        setTdxPath(cfg.tdx_path || 'D:\\tdx')
        setSyncTdxPath(cfg.tdx_path || 'D:\\tdx')
        setMcpURL(cfg.mcp_url || 'http://127.0.0.1:8765')
        setSixDimSource(cfg.sixdim_source || defaultSixDimSource())
        // 同花顺官方数据源（独立于行情数据源选择）
        setThsEnabled(!!cfg.ths_source?.enabled)
        setThsApiKey(cfg.ths_source?.api_key || '')
        setThsBaseUrl(cfg.ths_source?.base_url || '')
      }
    } catch (e) {
      console.error('Failed to load config:', e)
    }

    // 加载系统信息
    try {
      const info = await callApp<any>('GetSystemInfo')
      setSystemInfo(info)
    } catch (e) {
      console.error('Failed to load system info:', e)
    }

    // 加载 QMT 配置与 XtQuant 文件夹状态
    try {
      const qmt = await callApp<any>('GetQMTConfig')
      if (qmt) {
        setQmtEnabled(!!qmt.enabled)
        setQmtPath(qmt.path || '')
        setQmtAccount(qmt.account || '')
        setQmtAccountType(qmt.account_type || 'STOCK')
        setQmtMiniQMT(qmt.mini_qmt !== false)
        setQmtStrategyName(qmt.strategy_name || 'QuantBot')
        setQmtStrategyPath(qmt.strategy_path || '')
        setQmtFolderStatus(qmt.folder_status || null)
        setQmtAutoExecution(!!qmt.auto_execution)
      }
    } catch (e) {
      console.error('Failed to load QMT config:', e)
    }
  }

  const handleTestQMT = async () => {
    setQmtTesting(true)
    setQmtTestResult(null)
    try {
      const result = await callApp<any>('TestQMTConnection')
      setQmtTestResult(result)
      setQmtFolderStatus(result)
    } catch (e: any) {
      setQmtTestResult({ success: false, message: e?.message || '测试失败' })
    }
    setQmtTesting(false)
  }

  // 盘中自动买卖实盘独立开关（立即保存并生效，不走"保存设置"按钮）
  const handleToggleAutoExecution = async () => {
    const next = !qmtAutoExecution
    try {
      await callApp<void>('SetQMTApplyAutoExecution', next)
      setQmtAutoExecution(next)
    } catch (e: any) {
      alert(e?.message || '设置失败')
    }
  }

  // ==================== 软件更新 ====================

  // 监听下载进度与后台静默检查发现的更新
  useEffect(() => {
    const offProgress = EventsOn('update:progress', (p: { percent?: number }) => {
      if (typeof p?.percent === 'number') setUpdateProgress(p.percent)
    })
    const offAvailable = EventsOn('update:available', (info: any) => {
      if (!info?.latest_version) return
      setUpdateInfo(info)
      setUpdateState('available')
      setUpdateError('')
    })
    // 启动静默检查可能早于本页加载：加载一次后端缓存状态
    callApp<any>('GetUpdateStatus')
      .then((st: any) => {
        if (st?.has_update && st?.latest_version) {
          setUpdateInfo({
            current_version: st.current_version,
            latest_version: st.latest_version,
            changelog: st.changelog,
            size: st.size,
          })
          setUpdateState('available')
        }
      })
      .catch(() => {})
    return () => {
      offProgress()
      offAvailable()
      EventsOff('update:progress')
      EventsOff('update:available')
    }
  }, [])

  const handleCheckUpdate = async () => {
    setUpdateState('checking')
    setUpdateError('')
    try {
      const info = await callApp<any>('CheckForUpdate')
      if (info?.has_update) {
        setUpdateInfo(info)
        setUpdateState('available')
      } else {
        setUpdateInfo({ current_version: info?.current_version })
        setUpdateState('idle')
      }
    } catch (e: any) {
      setUpdateError(e?.message || '检查更新失败')
      setUpdateState('error')
    }
  }

  const handleDownloadUpdate = async () => {
    setUpdateState('downloading')
    setUpdateProgress(0)
    setUpdateError('')
    try {
      await callApp<void>('DownloadUpdate')
      setUpdateState('ready')
    } catch (e: any) {
      setUpdateError(e?.message || '下载升级包失败')
      setUpdateState('error')
    }
  }

  const handleApplyUpdate = async () => {
    setUpdateError('')
    try {
      await callApp<void>('ApplyUpdate')
      // 主进程随即退出，由升级子进程完成替换并重启
    } catch (e: any) {
      setUpdateError(e?.message || '应用更新失败')
      setUpdateState('error')
    }
  }

  // 保存同花顺官方数据源配置（启用开关 + API Key + 服务地址），保存后无需重启
  const handleSaveTHS = async () => {
    setThsSaved(false)
    try {
      await callApp<void>('SetTHSConfig', thsEnabled, thsApiKey.trim(), thsBaseUrl.trim())
      setThsSaved(true)
    } catch (e: any) {
      setSaveError(`同花顺官方数据源: ${e?.message || '保存失败'}`)
    }
  }

  const handleSave = async () => {
    setSaveError('')
    setSaveSuccess(false)
    const errors: string[] = []

    // 逐项保存，单项失败不影响其他设置
    // 保存数据源设置（优先保存，因为用户最关心这个）
    try {
      await callApp<void>('SetDataProvider', dataProvider)
    } catch (e: any) {
      errors.push(`数据源: ${e?.message || '失败'}`)
    }

    // 保存通用设置
    try {
      await callApp<void>('SetMarket', market)
    } catch (e: any) {
      errors.push(`市场: ${e?.message || '失败'}`)
    }

    try {
      await callApp<void>('SetTradingMode', tradingMode)
    } catch (e: any) {
      errors.push(`交易模式: ${e?.message || '失败'}`)
    }

    // 保存 QMT (迅投 XtQuant) 实盘交易接口配置
    try {
      await callApp<void>('SetQMTConfig', qmtEnabled, qmtPath, qmtAccount, qmtAccountType, qmtMiniQMT, qmtStrategyName, qmtStrategyPath)
    } catch (e: any) {
      errors.push(`QMT交易接口: ${e?.message || '失败'}`)
    }

    try {
      await callApp<void>('SetInitialCapital', initialCapital)
    } catch (e: any) {
      errors.push(`初始资金: ${e?.message || '失败'}`)
    }

    // 保存实时活动刷新间隔（1-60分钟整数）
    const refreshVal = Number(activityRefreshMinutes)
    if (!Number.isInteger(refreshVal) || refreshVal < 1 || refreshVal > 60) {
      errors.push('实时活动刷新间隔: 请输入1-60之间的整数')
    } else {
      try {
        await callApp<void>('SetActivityRefreshMinutes', refreshVal)
      } catch (e: any) {
        errors.push(`实时活动刷新间隔: ${e?.message || '失败'}`)
      }
    }

    // 保存 AI 设置
    try {
      await callApp<void>('SetAISettings', aiProvider, aiModel, aiBaseURL, apiKey)
    } catch (e: any) {
      errors.push(`AI设置: ${e?.message || '失败'}`)
    }

    // 保存其他数据源配置
    try {
      await callApp<void>('SetTDXPath', tdxPath)
    } catch (e: any) {
      errors.push(`TDX路径: ${e?.message || '失败'}`)
    }

    try {
      await callApp<void>('SetMCPConfig', mcpURL, mcpAPIKey)
    } catch (e: any) {
      errors.push(`MCP: ${e?.message || '失败'}`)
    }

    // 保存市场六维判势数据源配置（启用开关+优先级，持久化到 config.json）
    try {
      await callApp<void>('SetSixDimSourceConfig', sixDimSource)
    } catch (e: any) {
      errors.push(`六维判势数据源: ${e?.message || '失败'}`)
    }

    // 保存同花顺官方数据源配置（启用开关 + API Key + 服务地址）
    try {
      await callApp<void>('SetTHSConfig', thsEnabled, thsApiKey, thsBaseUrl)
    } catch (e: any) {
      errors.push(`同花顺官方数据源: ${e?.message || '失败'}`)
    }

    if (errors.length > 0) {
      setSaveError(errors.join('; '))
      console.error('Save errors:', errors)
    } else {
      setSaveSuccess(true)
      setTimeout(() => setSaveSuccess(false), 3000)
    }
  }

  const handleTestDataProvider = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const result = await callApp<any>('TestDataProvider', dataProvider)
      setTestResult(result)
    } catch (e: any) {
      setTestResult({ success: false, message: e?.message || e })
    }
    setTesting(false)
  }

  // 市场六维判势数据源「数据测试」：绕过缓存逐个探测各数据源连通性
  const handleTestSixDimSources = async () => {
    setSixDimTesting(true)
    setSixDimTestResult(null)
    try {
      const result = await callApp<any>('TestSixDimSources')
      setSixDimTestResult(result)
    } catch (e: any) {
      setSixDimTestResult({ ok_count: 0, total: 0, probes: [], note: '', error: e?.message || e })
    }
    setSixDimTesting(false)
  }

  // 市场六维判势「后台强制刷新」：穿透缓存重新判势并落库，前端判势卡片自动更新
  const handleRefreshSixDim = async () => {
    setSixDimRefreshing(true)
    setSixDimRefreshResult(null)
    try {
      const result = await callApp<any>('RefreshMarketSixDim')
      setSixDimRefreshResult(result)
    } catch (e: any) {
      setSixDimRefreshResult({ error: e?.message || e })
    }
    setSixDimRefreshing(false)
  }

  const handleConnectNativeTDX = async () => {
    if (nativeTDXConnected) {
      setNativeTDXConnected(false)
      setNativeTDXResult({ success: true, message: '已断开连接' })
      return
    }
    setNativeTDXConnecting(true)
    setNativeTDXResult(null)
    try {
      const result = await callApp<any>('ConnectNativeTDX')
      if (result?.status === 'connected') {
        setNativeTDXConnected(true)
        setNativeTDXResult({ success: true, message: result.message || '连接成功' })
      } else {
        setNativeTDXResult({ success: false, message: result?.message || '连接失败' })
      }
    } catch (e: any) {
      setNativeTDXResult({ success: false, message: e?.message || '连接失败' })
    }
    setNativeTDXConnecting(false)
  }

  const handleTestAI = async () => {
    setTestingAI(true)
    setAiTestResult(null)
    try {
      const result = await callApp<any>('TestAIConnection', aiProvider, aiModel, aiBaseURL, apiKey)
      
      // 测试成功后自动保存配置
      if (result.success) {
        try {
          await callApp<void>('SetAISettings', aiProvider, aiModel, aiBaseURL, apiKey)
          setSaved(true)
          setTimeout(() => setSaved(false), 2000)
        } catch (saveErr) {
          console.error('Auto save after test failed:', saveErr)
        }
      }
      
      setAiTestResult(result)
    } catch (e: any) {
      setAiTestResult({ status: 'error', message: e?.message || '请求失败' })
    }
    setTestingAI(false)
  }

  // ==================== 数据维护 ====================

  // 轮询同步任务状态
  useEffect(() => {
    if (!syncPolling) return
    let cancelled = false
    let timer: number | null = null
    // 顺序轮询：本次完成后再安排下一次，避免请求重叠；同步结束后自动停止
    const poll = async () => {
      if (cancelled) return
      try {
        const st = await callApp<any>('GetStockSyncStatus')
        if (cancelled) return
        setSyncStatus(st)
        if (st?.running) {
          timer = window.setTimeout(poll, 1500)
        } else {
          setSyncPolling(false)
        }
      } catch (e) {
        console.error('Failed to poll sync status:', e)
        if (!cancelled) timer = window.setTimeout(poll, 1500)
      }
    }
    timer = window.setTimeout(poll, 1500)
    return () => {
      cancelled = true
      if (timer !== null) window.clearTimeout(timer)
    }
  }, [syncPolling])

  const handleStartSync = async () => {
    try {
      setSyncStatus({ running: true, message: '正在启动同步...' })
      await callApp<void>('StartStockSync', syncTdxPath)
      setSyncPolling(true)
    } catch (e: any) {
      setSyncStatus({ running: false, error: e?.message || '启动同步失败' })
    }
  }

  // 财务数据同步
  useEffect(() => {
    if (!finSyncPolling) return
    let cancelled = false
    let timer: number | null = null
    // 顺序轮询：本次完成后再安排下一次，避免请求重叠；同步结束后自动停止
    const poll = async () => {
      if (cancelled) return
      try {
        const st = await callApp<any>('GetFinancialSyncStatus')
        if (cancelled) return
        setFinSyncStatus(st)
        if (st?.running) {
          timer = window.setTimeout(poll, 1500)
        } else {
          setFinSyncPolling(false)
        }
      } catch (e) {
        console.error('Failed to poll financial sync status:', e)
        if (!cancelled) timer = window.setTimeout(poll, 1500)
      }
    }
    timer = window.setTimeout(poll, 1500)
    return () => {
      cancelled = true
      if (timer !== null) window.clearTimeout(timer)
    }
  }, [finSyncPolling])

  const handleStartFinancialSync = async (mode: string, source: string) => {
    try {
      setFinSyncStatus({ running: true, message: '正在启动财务数据同步...' })
      await callApp<void>('StartFinancialSync', mode, source)
      setFinSyncPolling(true)
    } catch (e: any) {
      setFinSyncStatus({ running: false, error: e?.message || '启动财务同步失败' })
    }
  }

  // 可选：进入页面时静默拉取一次财务同步状态（已选模式展示历史记录）
  useEffect(() => {
    ;(async () => {
      try {
        const st = await callApp<any>('GetFinancialSyncStatus')
        setFinSyncStatus(st)
      } catch (e) {
        // 忽略：仅用于回填历史状态
      }
    })()
  }, [])

  // 同花顺复权因子导入 + 状态 + 前复权查询
  const loadAdjStatus = async () => {
    try {
      const st = await callApp<any>('GetTHSAdjFactorStatus')
      setAdjStatus(st)
    } catch (e) {
      // 忽略：未配置/未初始化时静默
    }
  }
  useEffect(() => {
    loadAdjStatus()
  }, [])
  const handleImportAdjFactors = async () => {
    if (adjBusy) return
    setAdjBusy(true)
    setAdjErr(null)
    setAdjResult(null)
    try {
      const res = await callApp<any>('ImportTHSAdjFactors')
      setAdjResult(res)
      await loadAdjStatus()
    } catch (e: any) {
      setAdjErr(e?.message || '导入复权因子失败')
    }
    setAdjBusy(false)
  }
  // 一键同步全部：先导入全市场复权因子，再同步交易日历，一次完成
  const [syncAllBusy, setSyncAllBusy] = useState(false)
  const handleSyncAll = async () => {
    if (syncAllBusy) return
    setSyncAllBusy(true)
    setAdjErr(null)
    setAdjResult(null)
    setCalErr(null)
    setCalResult(null)
    try {
      // 步骤1：复权因子导入
      const adjRes = await callApp<any>('ImportTHSAdjFactors')
      setAdjResult(adjRes)
      await loadAdjStatus()
      let steps = ['✓ 复权因子导入：' + (adjRes?.message || '完成')]
      // 步骤2：交易日历同步（失败不影响已完成的复权因子，但仍提示）
      try {
        const calRes = await callApp<any>('SyncTHSTradingCalendar')
        setCalResult(calRes)
        await loadCalStatus()
        steps.push('✓ 交易日历同步：' + (calRes?.message || '完成'))
      } catch (ce: any) {
        setCalErr(ce?.message || '同步交易日历失败')
        steps.push('✗ 交易日历同步：' + (ce?.message || '同步失败'))
      }
      setAdjResult({ ...adjRes, _steps: steps })
    } catch (e: any) {
      setAdjErr(e?.message || '一键同步失败')
    }
    setSyncAllBusy(false)
  }
  const loadDailyKStatus = async () => {
    try {
      const st = await callApp<any>('GetTHSDailyKStatus')
      setDailyKStatus(st)
    } catch (e) {
      // 忽略：未配置/未初始化时静默
    }
  }
  useEffect(() => {
    loadDailyKStatus()
  }, [])
  const handleImportDailyK = async (mode: 'full' | 'incr') => {
    if (dailyKBusy) return
    setDailyKBusy(true)
    setDailyKErr(null)
    setDailyKResult(null)
    try {
      const res = await callApp<any>('ImportTHSDailyK', mode)
      setDailyKResult(res)
      await loadDailyKStatus()
    } catch (e: any) {
      setDailyKErr(e?.message || '导入日K失败')
    }
    setDailyKBusy(false)
  }
  const loadCalStatus = async () => {
    try {
      const st = await callApp<any>('GetTHSTradingCalendarStatus')
      setCalStatus(st)
    } catch (e) {
      // 忽略：未配置/未初始化时静默
    }
  }
  useEffect(() => {
    loadCalStatus()
  }, [])
  // 同花顺「财务数据」同步导入（source=ths）
  const [thsFinSync, setThsFinSync] = useState<any>({})
  const handleStartTHSFinancialSync = async (mode: string) => {
    if (thsFinSync?.running) return
    setThsFinSync({ running: true, message: '正在启动同花顺财务数据同步...' })
    try {
      await callApp<void>('StartFinancialSync', mode, 'ths')
    } catch (e: any) {
      setThsFinSync({ running: false, error: e?.message || '启动同花顺财务同步失败' })
    }
  }
  const pollTHSFinSync = async () => {
    try {
      const st = await callApp<any>('GetFinancialSyncStatus')
      setThsFinSync(st)
    } catch (e) {
      // 忽略
    }
  }
  useEffect(() => {
    pollTHSFinSync()
    const t = setInterval(pollTHSFinSync, 5000)
    return () => clearInterval(t)
  }, [])

  const handleReset = async () => {
    if (resetBusy) return
    setResetBusy(true)
    setResetResult(null)
    try {
      const res = await callApp<any>('ResetUserData', resetConfirm)
      setResetResult(res)
      setResetConfirm('')
    } catch (e: any) {
      setResetResult({ error: e?.message || '系统初始化失败' })
    }
    setResetBusy(false)
  }

  const handleCleanup = async () => {
    if (cleanupBusy) return
    setCleanupBusy(true)
    setCleanupResult(null)
    try {
      const res = await callApp<any>('CleanupOldData')
      setCleanupResult(res)
    } catch (e: any) {
      setCleanupResult({ error: e?.message || '清理失败' })
    }
    setCleanupBusy(false)
  }

  // 加载系统健康度（每次点击都真实调用后端检查）
  const loadHealth = async () => {
    setHealthLoading(true)
    setHealthError(null)
    const start = Date.now()
    try {
      const res = await callApp<any>('GetSystemHealth')
      setHealth({ ...res, duration: ((Date.now() - start) / 1000).toFixed(1) })
    } catch (e: any) {
      setHealthError(e?.message || '获取系统健康度失败')
    } finally {
      setHealthLoading(false)
    }
  }

  useEffect(() => {
    if (activeTab === 'health') {
      loadHealth()
    }
  }, [activeTab])

  const [maintTab, setMaintTab] = useState<'ths' | 'tdx' | 'sysinit' | 'log'>('ths')

  const tabs = [
    { key: 'general' as TabKey, icon: SettingsIcon, label: t('settings.general') },
    { key: 'trading' as TabKey, icon: ArrowRightLeft, label: t('settings.tradingInterface') },
    { key: 'ai' as TabKey, icon: Cpu, label: t('settings.aiProvider') },
    { key: 'datasource' as TabKey, icon: Database, label: t('settings.dataSource') },
    { key: 'maintenance' as TabKey, icon: Wrench, label: '数据维护' },
    { key: 'audit' as TabKey, icon: FileText, label: t('settings.auditLog') },
    { key: 'health' as TabKey, icon: HeartPulse, label: '系统健康' },
    { key: 'contact' as TabKey, icon: HeartHandshake, label: '联系与捐赠' },
    { key: 'about' as TabKey, icon: Info, label: '关于' },
  ]

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3">
          <div className="w-10 h-10 rounded-xl bg-gradient-to-br from-purple-500 to-indigo-500 flex items-center justify-center">
            <SettingsIcon className="w-5 h-5 text-white" />
          </div>
          <h1 className="text-2xl font-bold text-slate-800 dark:text-slate-100">{t('settings.title')}</h1>
        </div>
        {saved && (
          <span className="text-sm text-green-600 dark:text-green-400 flex items-center gap-1">
            <Save className="w-4 h-4" />
            {t('settings.saved')}
          </span>
        )}
      </div>

      <div className="flex gap-5">
        {/* Tabs */}
        <div className="w-40 shrink-0 space-y-0.5">
          {tabs.map((tab) => (
            <button
              key={tab.key}
              onClick={() => setActiveTab(tab.key)}
              className={`w-full flex items-center gap-2 px-2.5 py-1.5 rounded-md text-xs font-medium transition-colors ${
                activeTab === tab.key
                  ? 'bg-brand-50 text-brand-700 dark:bg-brand-900/30 dark:text-brand-300'
                  : 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800'
              }`}
            >
              <tab.icon className="w-3.5 h-3.5" />
              {tab.label}
            </button>
          ))}
        </div>

        {/* Content */}
        <div className="flex-1 card p-5">
          {activeTab === 'general' && (
            <div className="space-y-5">
              <h2 className="text-base font-semibold text-slate-800 dark:text-slate-100">{t('settings.general')}</h2>

              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                    {t('settings.market')}
                  </label>
                  <button
                    disabled
                    className="w-full px-2.5 py-1.5 rounded-md text-xs font-medium bg-brand-500 text-white opacity-80 cursor-not-allowed"
                  >
                    {t('settings.cn')} {t('settings.aStockOnly')}
                  </button>
                </div>

                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                    {t('settings.language')}
                  </label>
                  <div className="flex gap-1.5">
                    {(['zh', 'en'] as Language[]).map((lang) => (
                      <button
                        key={lang}
                        onClick={() => setLanguage(lang)}
                        className={`flex-1 px-2.5 py-1.5 rounded-md text-xs font-medium transition-colors ${
                          language === lang
                            ? 'bg-brand-500 text-white'
                            : 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-700 dark:text-slate-300 dark:hover:bg-slate-600'
                        }`}
                      >
                        {lang === 'zh' ? '中文' : 'English'}
                      </button>
                    ))}
                  </div>
                </div>
              </div>

              <div className="grid grid-cols-2 gap-4">
                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                    {t('settings.initialCapital')}
                  </label>
                  <input
                    type="number"
                    value={initialCapital}
                    onChange={(e) => {
                      const v = Number(e.target.value)
                      if (v > 2000000) {
                        setInitialCapital(2000000)
                        return
                      }
                      setInitialCapital(v)
                    }}
                    className="input-field"
                    min={0}
                    max={2000000}
                    step={10000}
                  />
                  <p className="mt-1 text-[11px] text-slate-400 dark:text-slate-500">最高 200 万</p>
                </div>

                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                    {t('settings.theme')}
                  </label>
                  <div className="flex gap-1.5">
                    <button
                      onClick={() => setTheme('light')}
                      className={`flex-1 px-2.5 py-1.5 rounded-md text-xs font-medium transition-colors flex items-center justify-center gap-1 ${
                        theme === 'light'
                          ? 'bg-brand-500 text-white'
                          : 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-700 dark:text-slate-300 dark:hover:bg-slate-600'
                      }`}
                    >
                      ☀️ {t('settings.light')}
                    </button>
                    <button
                      onClick={() => setTheme('dark')}
                      className={`flex-1 px-2.5 py-1.5 rounded-md text-xs font-medium transition-colors flex items-center justify-center gap-1 ${
                        theme === 'dark'
                          ? 'bg-brand-500 text-white'
                          : 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-700 dark:text-slate-300 dark:hover:bg-slate-600'
                      }`}
                    >
                      🌙 {t('settings.dark')}
                    </button>
                  </div>
                </div>
              </div>

              {/* 实时活动刷新间隔设置 */}
              <div className="p-4 bg-slate-50 dark:bg-slate-800/50 rounded-lg space-y-2">
                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                    {t('settings.activityRefresh')}
                  </label>
                  <div className="flex items-center gap-3">
                    <input
                      type="number"
                      min={1}
                      max={60}
                      step={1}
                      value={activityRefreshMinutes}
                      onChange={(e) => setActivityRefreshMinutes(Number(e.target.value))}
                      className="input-field w-32"
                    />
                    <span className="text-xs text-slate-500 dark:text-slate-400">{t('settings.activityRefreshHint')}</span>
                  </div>
                  <p className="text-xs text-slate-500 dark:text-slate-400 mt-1.5">
                    {t('settings.activityRefreshDesc')}
                  </p>
                </div>
              </div>

              <div className="pt-3 border-t border-slate-200 dark:border-slate-700">
                <button onClick={handleSave} className="btn-primary">
                  {t('settings.save')}
                </button>
              </div>
            </div>
          )}

          {activeTab === 'trading' && (
            <div className="space-y-5">
              <h2 className="text-base font-semibold text-slate-800 dark:text-slate-100">{t('settings.tradingInterface')}</h2>

              {/* 交易模式设置 */}
              <div className="p-4 bg-slate-50 dark:bg-slate-800/50 rounded-lg space-y-3">
                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-2">
                    {t('settings.tradingMode')}
                  </label>
                  <div className="flex gap-2">
                    <button
                      onClick={() => setTradingMode('simulated')}
                      className={`flex-1 px-3 py-2 rounded-md text-xs font-medium transition-colors ${
                        tradingMode === 'simulated'
                          ? 'bg-blue-500 text-white'
                          : 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-700 dark:text-slate-300 dark:hover:bg-slate-600'
                      }`}
                    >
                      {t('settings.simulatedInterface')}
                    </button>
                    <button
                      onClick={() => setTradingMode('live')}
                      className={`flex-1 px-3 py-2 rounded-md text-xs font-medium transition-colors ${
                        tradingMode === 'live'
                          ? 'bg-red-500 text-white'
                          : 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-700 dark:text-slate-300 dark:hover:bg-slate-600'
                      }`}
                    >
                      {t('settings.liveInterface')}
                    </button>
                  </div>
                  <p className="text-xs text-slate-500 dark:text-slate-400 mt-2">
                    {tradingMode === 'simulated'
                      ? t('settings.simulatedInterfaceDesc')
                      : t('settings.liveInterfaceDesc')}
                  </p>
                </div>

                {tradingMode === 'live' && (
                  <div className="p-3 bg-amber-50 dark:bg-amber-900/20 rounded-md border border-amber-200 dark:border-amber-800">
                    <div className="flex items-center gap-2 text-xs text-amber-700 dark:text-amber-400">
                      <AlertTriangle className="w-4 h-4 shrink-0" />
                      <span className="font-medium">{t('settings.liveWarning')}</span>
                    </div>
                    <p className="text-xs text-amber-600 dark:text-amber-500 mt-1">
                      {t('settings.liveWarningDesc')}
                    </p>
                  </div>
                )}
              </div>

              {/* QMT (迅投 XtQuant) 实盘交易接口设置 */}
              {tradingMode === 'live' && (
                <div className="p-4 bg-slate-50 dark:bg-slate-800/50 rounded-lg space-y-4">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2">
                      <Zap className="w-4 h-4 text-amber-500" />
                      <h3 className="text-sm font-semibold text-slate-800 dark:text-slate-100">{t('settings.qmtTitle')}</h3>
                    </div>
                    <button
                      onClick={() => setQmtEnabled(!qmtEnabled)}
                      className={`relative w-10 h-5 rounded-full transition-colors ${qmtEnabled ? 'bg-green-500' : 'bg-slate-300 dark:bg-slate-600'}`}
                    >
                      <span className={`absolute top-0.5 w-4 h-4 rounded-full bg-white shadow transition-all ${qmtEnabled ? 'left-5' : 'left-0.5'}`} />
                    </button>
                  </div>
                  <p className="text-xs text-slate-500 dark:text-slate-400">{t('settings.qmtDesc')}</p>

                  {qmtEnabled && (
                    <div className="space-y-3">
                      {/* XtQuant 路径 */}
                      <div>
                        <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                          {t('settings.qmtPath')}
                        </label>
                        <div className="flex gap-2">
                          <input
                            type="text"
                            value={qmtPath}
                            onChange={(e) => setQmtPath(e.target.value)}
                            placeholder="D:\\qmt\\userdata_mini"
                            className="input-field flex-1 font-mono"
                          />
                          <button
                            onClick={async () => {
                              try {
                                const r = await callApp<any>('GetXtQuantFolderPath')
                                if (r?.path) setQmtPath(r.path)
                              } catch { /* ignore */ }
                            }}
                            className="px-3 py-2 rounded-md text-xs font-medium bg-slate-200 dark:bg-slate-700 text-slate-700 dark:text-slate-200 hover:bg-slate-300 dark:hover:bg-slate-600 transition-colors flex items-center gap-1"
                          >
                            <FolderOpen className="w-3.5 h-3.5" />
                            {t('settings.qmtUseFolder')}
                          </button>
                        </div>
                        <p className="text-xs text-slate-500 dark:text-slate-400 mt-1">{t('settings.qmtPathHint')}</p>
                      </div>

                      {/* 资金账号 */}
                      <div className="grid grid-cols-2 gap-3">
                        <div>
                          <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                            {t('settings.qmtAccount')}
                          </label>
                          <input
                            type="text"
                            value={qmtAccount}
                            onChange={(e) => setQmtAccount(e.target.value)}
                            placeholder="资金账号"
                            className="input-field font-mono"
                          />
                        </div>
                        <div>
                          <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                            {t('settings.qmtAccountType')}
                          </label>
                          <select value={qmtAccountType} onChange={(e) => setQmtAccountType(e.target.value)} className="input-field">
                            <option value="STOCK">STOCK（股票）</option>
                            <option value="CREDIT">CREDIT（信用）</option>
                          </select>
                        </div>
                      </div>

                      {/* MiniQMT 模式 */}
                      <div className="flex items-center justify-between p-3 bg-white dark:bg-slate-800 rounded-lg border border-slate-200 dark:border-slate-700">
                        <div>
                          <p className="text-xs font-medium text-slate-700 dark:text-slate-300">{t('settings.qmtMiniQMT')}</p>
                          <p className="text-[11px] text-slate-500 dark:text-slate-400 mt-0.5">{t('settings.qmtMiniQMTDesc')}</p>
                        </div>
                        <button
                          onClick={() => setQmtMiniQMT(!qmtMiniQMT)}
                          className={`relative w-10 h-5 rounded-full transition-colors shrink-0 ${qmtMiniQMT ? 'bg-green-500' : 'bg-slate-300 dark:bg-slate-600'}`}
                        >
                          <span className={`absolute top-0.5 w-4 h-4 rounded-full bg-white shadow transition-all ${qmtMiniQMT ? 'left-5' : 'left-0.5'}`} />
                        </button>
                      </div>

                      {/* 盘中自动买卖实盘独立开关：立即生效，独立于手动/确认下单 */}
                      <div className="flex items-center justify-between p-3 bg-amber-50/60 dark:bg-amber-900/10 rounded-lg border border-amber-200 dark:border-amber-800">
                        <div className="pr-3">
                          <p className="text-xs font-medium text-slate-700 dark:text-slate-300">{t('settings.qmtAutoExecution')}</p>
                          <p className="text-[11px] text-slate-500 dark:text-slate-400 mt-0.5">{t('settings.qmtAutoExecutionDesc')}</p>
                        </div>
                        <button
                          onClick={handleToggleAutoExecution}
                          className={`relative w-10 h-5 rounded-full transition-colors shrink-0 ${qmtAutoExecution ? 'bg-green-500' : 'bg-slate-300 dark:bg-slate-600'}`}
                        >
                          <span className={`absolute top-0.5 w-4 h-4 rounded-full bg-white shadow transition-all ${qmtAutoExecution ? 'left-5' : 'left-0.5'}`} />
                        </button>
                      </div>

                      {/* 策略设置 */}
                      <div className="grid grid-cols-2 gap-3">
                        <div>
                          <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                            {t('settings.qmtStrategyName')}
                          </label>
                          <input
                            type="text"
                            value={qmtStrategyName}
                            onChange={(e) => setQmtStrategyName(e.target.value)}
                            placeholder="QuantBot"
                            className="input-field"
                          />
                        </div>
                        <div>
                          <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                            {t('settings.qmtStrategyPath')}
                          </label>
                          <input
                            type="text"
                            value={qmtStrategyPath}
                            onChange={(e) => setQmtStrategyPath(e.target.value)}
                            placeholder="策略路径（可选）"
                            className="input-field font-mono"
                          />
                        </div>
                      </div>

                      {/* 测试连接 */}
                      <div className="pt-2">
                        <button
                          onClick={handleTestQMT}
                          disabled={qmtTesting}
                          className="px-4 py-2 rounded-md text-xs font-medium bg-indigo-500 hover:bg-indigo-600 text-white disabled:opacity-50 transition-colors flex items-center gap-1.5"
                        >
                          <TestTube className="w-3.5 h-3.5" />
                          {qmtTesting ? t('settings.qmtTesting') : t('settings.qmtTest')}
                        </button>
                        {qmtTestResult && (
                          <div className={`mt-3 p-3 rounded-md text-xs ${qmtTestResult.success ? 'bg-green-50 dark:bg-green-900/20 text-green-700 dark:text-green-400 border border-green-200 dark:border-green-800' : 'bg-red-50 dark:bg-red-900/20 text-red-700 dark:text-red-400 border border-red-200 dark:border-red-800'}`}>
                            <div className="flex items-center gap-1.5 mb-1">
                              {qmtTestResult.success ? <CheckCircle className="w-3.5 h-3.5" /> : <AlertTriangle className="w-3.5 h-3.5" />}
                              <span className="font-medium">{qmtTestResult.message || (qmtTestResult.success ? '连接正常' : '连接失败')}</span>
                            </div>
                            <div className="space-y-0.5 text-[11px] opacity-90">
                              <div>XtQuant 文件夹：{qmtTestResult.folder_exists ? '已创建' : '未创建'}（{qmtTestResult.folder_path || '-'}）</div>
                              <div>Python 环境：{qmtTestResult.python_exists ? '已安装' : '未检测到'}</div>
                              <div>XtQuant 库：{qmtTestResult.xtquant_exists ? '已安装' : '未检测到'}</div>
                              {qmtTestResult.interface_files?.length > 0 && (
                                <div>接口文件：{qmtTestResult.interface_files.join(', ')}</div>
                              )}
                            </div>
                          </div>
                        )}
                      </div>
                    </div>
                  )}
                </div>
              )}

              <div className="pt-3 border-t border-slate-200 dark:border-slate-700">
                <button onClick={handleSave} className="btn-primary">
                  {t('settings.save')}
                </button>
              </div>
            </div>
          )}

          {activeTab === 'ai' && (
            <div className="space-y-4">
              <h2 className="text-base font-semibold text-slate-800 dark:text-slate-100">{t('settings.aiConfig')}</h2>

              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                  {t('settings.aiProvider')}
                </label>
                <select value={aiProvider} onChange={(e) => setAiProvider(e.target.value)} className="input-field">
                  <option value="deepseek">DeepSeek</option>
                  <option value="openai">OpenAI</option>
                  <option value="claude">Claude</option>
                  <option value="other">Other</option>
                </select>
              </div>

              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                  {t('settings.model')}
                </label>
                <input
                  type="text"
                  value={aiModel}
                  onChange={(e) => setAiModel(e.target.value)}
                  className="input-field"
                  placeholder="deepseek-chat"
                />
              </div>

              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                  {t('settings.baseURL')}
                </label>
                <input
                  type="text"
                  value={aiBaseURL}
                  onChange={(e) => setAiBaseURL(e.target.value)}
                  className="input-field"
                  placeholder="https://api.deepseek.com"
                />
              </div>

              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                  {t('settings.apiKey')}
                </label>
                <div className="relative">
                  <Key className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-400" />
                  <input
                    type="password"
                    value={apiKey}
                    onChange={(e) => setApiKey(e.target.value)}
                    placeholder="sk-..."
                    className="input-field pl-10"
                  />
                </div>
                <p className="text-xs text-slate-500 dark:text-slate-400 mt-1">
                  {t('settings.apiKeyHint')}
                </p>
              </div>

              <div className="pt-3 border-t border-slate-200 dark:border-slate-700">
                <div className="flex gap-2">
                  <button onClick={handleSave} className="btn-primary">
                    {t('settings.save')}
                  </button>
                  <button
                    onClick={handleTestAI}
                    disabled={testingAI}
                    className="px-2.5 py-1.5 rounded-md text-xs font-medium border border-slate-300 dark:border-slate-600 text-slate-700 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors flex items-center gap-1.5 disabled:opacity-50"
                  >
                    <TestTube className="w-3.5 h-3.5" />
                    {testingAI ? '测试中...' : '测试连接'}
                  </button>
                </div>

                {aiTestResult && (
                  <div className={`mt-3 p-3 rounded-md text-xs ${
                    aiTestResult.success
                      ? 'bg-green-50 dark:bg-green-900/20 text-green-700 dark:text-green-400 border border-green-200 dark:border-green-800'
                      : 'bg-red-50 dark:bg-red-900/20 text-red-700 dark:text-red-400 border border-red-200 dark:border-red-800'
                  }`}>
                    <div className="font-medium mb-1.5 flex items-center gap-1.5">
                      {aiTestResult.success ? (
                        <>
                          <span className="text-green-600">✓</span>
                          <span>连接成功</span>
                        </>
                      ) : (
                        <>
                          <span className="text-red-600">✗</span>
                          <span>连接失败</span>
                        </>
                      )}
                    </div>
                    {aiTestResult.success ? (
                      <div className="space-y-1 text-xs">
                        <p><span className="font-medium">模型：</span>{aiTestResult.model}</p>
                        <p><span className="font-medium">响应时间：</span>{aiTestResult.elapsed_ms}ms</p>
                        <p><span className="font-medium">AI回复：</span>{aiTestResult.response}</p>
                      </div>
                    ) : (
                      <div className="space-y-1 text-xs">
                        <p><span className="font-medium">错误：</span>{aiTestResult.message}</p>
                        {aiTestResult.error_type && (
                          <p className="text-slate-500 dark:text-slate-400">
                            <span className="font-medium">错误类型：</span>{aiTestResult.error_type}
                          </p>
                        )}
                        <p className="text-slate-500 dark:text-slate-400 mt-2">
                          💡 请检查 API Key 是否正确，模型名是否有效，以及网络连接是否正常。
                        </p>
                      </div>
                    )}
                  </div>
                )}
              </div>
            </div>
          )}

          {activeTab === 'datasource' && (
            <div className="space-y-4">
              <h2 className="text-base font-semibold text-slate-800 dark:text-slate-100">{t('settings.dataSource')}</h2>

              {/* 数据源页内部页签：行情数据 / 市场信息数据 */}
              <div className="flex border-b border-slate-200 dark:border-slate-700">
                <button
                  onClick={() => setDsTab('realtime')}
                  className={`px-4 py-2 text-xs font-medium border-b-2 transition-colors ${
                    dsTab === 'realtime'
                      ? 'border-brand-500 text-brand-600 dark:text-brand-400'
                      : 'border-transparent text-slate-500 dark:text-slate-400 hover:text-slate-700 dark:hover:text-slate-300'
                  }`}
                >
                  行情数据
                </button>
                <button
                  onClick={() => setDsTab('aux')}
                  className={`px-4 py-2 text-xs font-medium border-b-2 transition-colors ${
                    dsTab === 'aux'
                      ? 'border-brand-500 text-brand-600 dark:text-brand-400'
                      : 'border-transparent text-slate-500 dark:text-slate-400 hover:text-slate-700 dark:hover:text-slate-300'
                  }`}
                >
                  市场信息数据
                </button>
              </div>

              {dsTab === 'realtime' && (
              <>
              {/* 数据源类型选择 */}
              <div>
                <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                  {t('settings.dataSourceType')}
                </label>
                <div className="grid grid-cols-2 md:grid-cols-3 gap-2">
                  <button
                    onClick={() => setDataProvider('native_tdx')}
                    className={`p-2.5 rounded-md border-2 text-xs font-medium transition-colors text-left ${
                      dataProvider === 'native_tdx'
                        ? 'border-brand-500 bg-brand-50 dark:bg-brand-900/20 dark:border-brand-400'
                        : 'border-slate-200 dark:border-slate-700 hover:border-slate-300 dark:hover:border-slate-600'
                    }`}
                  >
                    <div className="flex items-center gap-1.5">
                      <Zap className="w-3.5 h-3.5" />
                      <span>{t('settings.nativeTDX')}</span>
                    </div>
                    <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
                      {t('settings.nativeTDXDesc')}
                    </p>
                  </button>

                  <button
                    onClick={() => setDataProvider('tdx_mcp')}
                    className={`p-2.5 rounded-md border-2 text-xs font-medium transition-colors text-left ${
                      dataProvider === 'tdx_mcp'
                        ? 'border-brand-500 bg-brand-50 dark:bg-brand-900/20 dark:border-brand-400'
                        : 'border-slate-200 dark:border-slate-700 hover:border-slate-300 dark:hover:border-slate-600'
                    }`}
                  >
                    <div className="flex items-center gap-1.5">
                      <Cpu className="w-3.5 h-3.5" />
                      <span>{t('settings.tdxMCP')}</span>
                    </div>
                    <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
                      {t('settings.tdxMCPDesc')}
                    </p>
                  </button>

                  <button
                    onClick={() => setDataProvider('mcp')}
                    className={`p-2.5 rounded-md border-2 text-xs font-medium transition-colors text-left ${
                      dataProvider === 'mcp'
                        ? 'border-brand-500 bg-brand-50 dark:bg-brand-900/20 dark:border-brand-400'
                        : 'border-slate-200 dark:border-slate-700 hover:border-slate-300 dark:hover:border-slate-600'
                    }`}
                  >
                    <div className="flex items-center gap-1.5">
                      <Cpu className="w-3.5 h-3.5" />
                      <span>{t('settings.mcp')}</span>
                    </div>
                    <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
                      {t('settings.mcpDesc')}
                    </p>
                  </button>

                  <button
                    onClick={() => setDataProvider('tdx_terminal')}
                    className={`p-2.5 rounded-md border-2 text-xs font-medium transition-colors text-left ${
                      dataProvider === 'tdx_terminal'
                        ? 'border-brand-500 bg-brand-50 dark:bg-brand-900/20 dark:border-brand-400'
                        : 'border-slate-200 dark:border-slate-700 hover:border-slate-300 dark:hover:border-slate-600'
                    }`}
                  >
                    <div className="flex items-center gap-1.5">
                      <Database className="w-3.5 h-3.5" />
                      <span>{t('settings.terminal')}</span>
                    </div>
                    <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
                      {t('settings.terminalDesc')}
                    </p>
                  </button>

                  <button
                    onClick={() => setDataProvider('tencent')}
                    className={`p-2.5 rounded-md border-2 text-xs font-medium transition-colors text-left ${
                      dataProvider === 'tencent'
                        ? 'border-brand-500 bg-brand-50 dark:bg-brand-900/20 dark:border-brand-400'
                        : 'border-slate-200 dark:border-slate-700 hover:border-slate-300 dark:hover:border-slate-600'
                    }`}
                  >
                    <div className="flex items-center gap-1.5">
                      <Globe className="w-3.5 h-3.5" />
                      <span>腾讯财经</span>
                    </div>
                    <p className="text-xs text-slate-500 dark:text-slate-400 mt-0.5">
                      实时行情走腾讯财经 API，无需本机通达信
                    </p>
                  </button>
                </div>
              </div>

              {/* TDX 本地路径配置 */}
              {(dataProvider === 'tdx_mcp' || dataProvider === 'tdx_terminal') && (
                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">
                    {t('settings.tdxPath')}
                  </label>
                  <input
                    type="text"
                    value={tdxPath}
                    onChange={(e) => setTdxPath(e.target.value)}
                    className="input-field"
                    placeholder="D:\\tdx"
                  />
                  <p className="text-xs text-slate-500 dark:text-slate-400 mt-1">
                    {dataProvider === 'tdx_terminal'
                      ? t('settings.terminalHint')
                      : t('settings.tdxPathHint')}
                  </p>
                </div>
              )}

              {/* MCP 配置 */}
              {(dataProvider === 'tdx_mcp' || dataProvider === 'mcp') && (
                <div className="space-y-3 p-3 bg-slate-50 dark:bg-slate-800/50 rounded-md">
                  <h3 className="text-xs font-medium text-slate-700 dark:text-slate-300">
                    {t('settings.mcpConfig')}
                  </h3>
                  <div>
                    <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">
                      {t('settings.mcpURL')}
                    </label>
                    <input
                      type="text"
                      value={mcpURL}
                      onChange={(e) => setMcpURL(e.target.value)}
                      className="input-field"
                      placeholder="http://127.0.0.1:8765"
                    />
                  </div>
                  <div>
                    <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1">
                      {t('settings.apiKey')}
                    </label>
                    <div className="relative">
                      <Key className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-400" />
                      <input
                        type="password"
                        value={mcpAPIKey}
                        onChange={(e) => setMcpAPIKey(e.target.value)}
                        placeholder="MCP API Key (optional)"
                        className="input-field pl-10"
                      />
                    </div>
                  </div>
                </div>
              )}

              {/* Go Native TDX 配置 */}
              {dataProvider === 'native_tdx' && (
                <div className="space-y-3 p-3 bg-slate-50 dark:bg-slate-800/50 rounded-md">
                  <div className="flex items-center justify-between">
                    <h3 className="text-xs font-medium text-slate-700 dark:text-slate-300 flex items-center gap-1.5">
                      <Zap className="w-3.5 h-3.5" />
                      {t('settings.nativeTDXConfig')}
                    </h3>
                    <div className={`flex items-center gap-1 text-xs ${
                      nativeTDXConnected
                        ? 'text-green-600 dark:text-green-400'
                        : 'text-slate-400'
                    }`}>
                      <span className={`w-1.5 h-1.5 rounded-full ${
                        nativeTDXConnected ? 'bg-green-500 animate-pulse' : 'bg-slate-400'
                      }`} />
                      {nativeTDXConnected ? t('settings.nativeTDXConnected') : t('settings.nativeTDXDisconnected')}
                    </div>
                  </div>

                  <div className="text-xs text-slate-500 dark:text-slate-400 p-2 bg-amber-50 dark:bg-amber-900/20 rounded">
                    💡 {t('settings.nativeTDXHint')}
                  </div>

                  <div className="flex gap-2">
                    <button
                      onClick={handleConnectNativeTDX}
                      disabled={nativeTDXConnecting}
                      className="px-3 py-1.5 rounded-md text-xs font-medium bg-brand-600 hover:bg-brand-700 text-white transition-colors flex items-center gap-1.5 disabled:opacity-50"
                    >
                      {nativeTDXConnecting ? (
                        <RefreshCw className="w-3.5 h-3.5 animate-spin" />
                      ) : (
                        <Zap className="w-3.5 h-3.5" />
                      )}
                      {nativeTDXConnected ? t('settings.nativeTDXDisconnect') : t('settings.nativeTDXConnect')}
                    </button>
                  </div>

                  {nativeTDXResult && (
                    <div className={`p-2 rounded-md text-xs ${
                      nativeTDXResult.success
                        ? 'bg-green-50 dark:bg-green-900/20 text-green-700 dark:text-green-400'
                        : 'bg-red-50 dark:bg-red-900/20 text-red-700 dark:text-red-400'
                    }`}>
                      {nativeTDXResult.message}
                    </div>
                  )}
                </div>
              )}

              {/* 腾讯财经数据源说明 */}
              {dataProvider === 'tencent' && (
                <div className="space-y-3 p-3 bg-slate-50 dark:bg-slate-800/50 rounded-md">
                  <h3 className="text-xs font-medium text-slate-700 dark:text-slate-300 flex items-center gap-1.5">
                    <Globe className="w-3.5 h-3.5" />
                    腾讯财经数据源
                  </h3>
                  <div className="text-xs text-slate-500 dark:text-slate-400 p-2 bg-amber-50 dark:bg-amber-900/20 rounded">
                    💡 腾讯财经用于读取<b>实时行情</b>数据（组合持仓、看板、指数、交易报价等）。研究中心的选股引擎、策略、回测仍使用本地 DuckDB 数据，不受数据源切换影响。
                  </div>
                  <div className="text-xs text-slate-500 dark:text-slate-400">
                    无需安装通达信，也无需任何额外配置。保存后即可生效，可点击下方「测试连接」验证腾讯财经实时行情是否可用。
                  </div>
                </div>
              )}

              {/* 实时行情：测试连接 + 保存 */}
              <div className="pt-3 border-t border-slate-200 dark:border-slate-700">
                <div className="flex gap-2">
                  <button
                    onClick={handleSave}
                    className="btn-primary"
                  >
                    {t('settings.save')}
                  </button>
                  <button
                    onClick={handleTestDataProvider}
                    disabled={testing}
                    className="px-2.5 py-1.5 rounded-md text-xs font-medium border border-slate-300 dark:border-slate-600 text-slate-700 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors flex items-center gap-1.5 disabled:opacity-50"
                  >
                    <TestTube className="w-3.5 h-3.5" />
                    {testing ? t('settings.testing') : t('settings.testConnection')}
                  </button>
                </div>
                {saveError && (
                  <div className="mt-2 p-2.5 rounded-md text-xs bg-red-50 dark:bg-red-900/20 text-red-700 dark:text-red-400 border border-red-200 dark:border-red-800">
                    <span className="font-medium">❌ 保存失败：</span>
                    <span className="ml-1">{saveError}</span>
                  </div>
                )}
                {saveSuccess && (
                  <div className="mt-2 p-2.5 rounded-md text-xs bg-green-50 dark:bg-green-900/20 text-green-700 dark:text-green-400 border border-green-200 dark:border-green-800">
                    <span className="font-medium">✅ 设置已保存</span>
                  </div>
                )}
                {testResult && (
                  <div className={`mt-3 p-3 rounded-md text-xs ${
                    testResult.success === true
                      ? 'bg-green-50 dark:bg-green-900/20 text-green-700 dark:text-green-400'
                      : 'bg-red-50 dark:bg-red-900/20 text-red-700 dark:text-red-400'
                  }`}>
                    <div className="font-medium mb-1.5">
                      {testResult.success === true ? '✓ ' + t('settings.testSuccess') : '✗ ' + t('settings.testFailed')}
                    </div>
                    <pre className="whitespace-pre-wrap text-xs">{JSON.stringify(testResult, null, 2)}</pre>
                  </div>
                )}
              </div>
              </>
              )}

              {dsTab === 'aux' && (
              <>
              {/* 辅助数据源卡片（并排）：市场六维判势 / 同花顺官方，选中不同卡片填写不同配置 */}
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                {/* 同花顺官方卡片 */}
                <div
                  onClick={() => { setThsOpen(true); setSixDimOpen(false) }}
                  className={`cursor-pointer p-3 rounded-md border-2 text-xs transition-colors ${
                    thsOpen
                      ? 'border-brand-500 bg-brand-50 dark:bg-brand-900/20 dark:border-brand-400'
                      : 'border-slate-200 dark:border-slate-700 hover:border-slate-300 dark:hover:border-slate-600'
                  }`}
                >
                  <div className="flex items-center gap-1.5 font-medium text-slate-700 dark:text-slate-300">
                    <Activity className="w-3.5 h-3.5" />
                    <span>同花顺官方金融数据服务</span>
                  </div>
                  <p className="text-xs text-slate-500 dark:text-slate-400 mt-1.5">
                    六维判势情绪 / 财务同步 / 复权因子 / 估值竞价 / 全市场日K。
                  </p>
                </div>
                {/* 市场六维判势卡片 */}
                <div
                  onClick={() => { setSixDimOpen(true); setThsOpen(false) }}
                  className={`cursor-pointer p-3 rounded-md border-2 text-xs transition-colors ${
                    sixDimOpen
                      ? 'border-brand-500 bg-brand-50 dark:bg-brand-900/20 dark:border-brand-400'
                      : 'border-slate-200 dark:border-slate-700 hover:border-slate-300 dark:hover:border-slate-600'
                  }`}
                >
                  <div className="flex items-center gap-1.5 font-medium text-slate-700 dark:text-slate-300">
                    <Activity className="w-3.5 h-3.5" />
                    <span>市场六维判势数据源</span>
                  </div>
                  <p className="text-xs text-slate-500 dark:text-slate-400 mt-1.5">
                    情绪/资金/外围实时源配置，与实时行情数据源选择相互独立。
                  </p>
                </div>
              </div>

              {/* 市场六维判势数据源配置（选中上方卡片后展开，优先级越小越先尝试，高优先级失败自动回退，配置保存在 config.json） */}
              {sixDimOpen && (
              <div className="space-y-3 p-3 bg-slate-50 dark:bg-slate-800/50 rounded-md">
                <div>
                  <h3 className="text-xs font-medium text-slate-700 dark:text-slate-300 flex items-center gap-1.5">
                    <Activity className="w-3.5 h-3.5" />
                    市场六维判势数据源
                  </h3>
                  <p className="text-xs text-slate-500 dark:text-slate-400 mt-1">
                    优先级数值越小越先尝试；高优先级源失败自动回退低优先级源（DuckDB 兜底），全程无伪造。保存后下一次判势立即生效。
                  </p>
                </div>
                <div className="space-y-2">
                  {Object.keys(sixDimSourceMeta).map((key) => (
                    <div key={key} className="flex items-center justify-between gap-2 p-2 rounded-md border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800/50">
                      <div className="min-w-0">
                        <div className="flex items-center gap-1.5 text-xs font-medium text-slate-700 dark:text-slate-300">
                          <span className="truncate">{sixDimSourceMeta[key].name}</span>
                        </div>
                        <p className="text-[11px] text-slate-400 dark:text-slate-500 mt-0.5 truncate">{sixDimSourceMeta[key].desc}</p>
                      </div>
                      <div className="flex items-center gap-2 shrink-0">
                        <label className="flex items-center gap-1 text-[11px] text-slate-500 dark:text-slate-400">
                          <input
                            type="checkbox"
                            checked={sixDimSource[key]?.enabled ?? true}
                            onChange={(e) => updateSixDimSource(key, 'enabled', e.target.checked)}
                            className="w-3.5 h-3.5 rounded border-slate-300 dark:border-slate-600 accent-brand-600"
                          />
                          启用
                        </label>
                        <label className="flex items-center gap-1 text-[11px] text-slate-500 dark:text-slate-400">
                          优先级
                          <input
                            type="number"
                            min={1}
                            max={9}
                            value={sixDimSource[key]?.priority ?? 1}
                            onChange={(e) => updateSixDimSource(key, 'priority', Math.max(1, Number(e.target.value) || 1))}
                            className="w-14 px-1.5 py-1 rounded-md text-xs border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-800 text-slate-700 dark:text-slate-300"
                          />
                        </label>
                      </div>
                    </div>
                  ))}
                </div>

                {/* 数据测试 + 后台强制刷新 */}
                <div className="pt-2 border-t border-slate-200 dark:border-slate-700">
                  <p className="text-[11px] text-slate-400 dark:text-slate-500 mb-2">
                    数据测试绕过缓存逐个探测各数据源连通性（外部源带 6s 超时+限流）；后台强制刷新穿透缓存重新判势并落库，前端判势卡片立即更新。
                  </p>
                  <div className="flex flex-wrap gap-2">
                    <button
                      onClick={handleTestSixDimSources}
                      disabled={sixDimTesting}
                      className="px-2.5 py-1.5 rounded-md text-xs font-medium border border-slate-300 dark:border-slate-600 text-slate-700 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors flex items-center gap-1.5 disabled:opacity-60"
                    >
                      {sixDimTesting ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <TestTube className="w-3.5 h-3.5" />}
                      {sixDimTesting ? '测试中...' : '数据测试'}
                    </button>
                    <button
                      onClick={handleRefreshSixDim}
                      disabled={sixDimRefreshing}
                      className="px-2.5 py-1.5 rounded-md text-xs font-medium border border-slate-300 dark:border-slate-600 text-slate-700 dark:text-slate-300 hover:bg-slate-50 dark:hover:bg-slate-800 transition-colors flex items-center gap-1.5 disabled:opacity-60"
                    >
                      {sixDimRefreshing ? <RefreshCw className="w-3.5 h-3.5 animate-spin" /> : <RefreshCw className="w-3.5 h-3.5" />}
                      {sixDimRefreshing ? '刷新中...' : '后台强制刷新'}
                    </button>
                  </div>

                  {/* 数据测试结果 */}
                  {sixDimTestResult && (
                    <div className="mt-3 space-y-1.5 rounded-md border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-800/50 p-2.5">
                      <div className="flex items-center justify-between text-xs font-medium text-slate-700 dark:text-slate-300">
                        <span className="flex items-center gap-1.5">
                          {sixDimTestResult.error ? <AlertTriangle className="w-3.5 h-3.5 text-red-500" /> : <CheckCircle className={`w-3.5 h-3.5 ${(sixDimTestResult.ok_count ?? 0) === (sixDimTestResult.total ?? 0) ? 'text-green-500' : 'text-amber-500'}`} />}
                          数据测试
                        </span>
                        {!sixDimTestResult.error && <span className="text-[11px] text-slate-400">连通 {sixDimTestResult.ok_count} / {sixDimTestResult.total}</span>}
                      </div>
                      {sixDimTestResult.error ? (
                        <p className="text-[11px] text-red-600 dark:text-red-400">{sixDimTestResult.error}</p>
                      ) : (
                        ((sixDimTestResult.probes ?? []) as Array<{ key: string; name: string; enabled: boolean; priority: number; kind: string; ok: boolean; message: string; latency_ms: number }>).map((p) => (
                          <div key={p.key} className="flex items-start justify-between gap-2 text-[11px]">
                            <div className="min-w-0">
                              <span className="font-medium text-slate-700 dark:text-slate-300 truncate">{p.name}</span>
                              <span className="ml-1.5 text-slate-400">{p.enabled ? `优先级${p.priority}` : '（禁用）'}</span>
                              <span className="block text-slate-400 truncate">{p.message}</span>
                            </div>
                            <div className="flex items-center gap-1.5 shrink-0">
                              {p.latency_ms > 0 && <span className="text-slate-400">{p.latency_ms}ms</span>}
                              <CheckCircle className={`w-3.5 h-3.5 ${p.ok ? 'text-green-500' : 'text-red-500'}`} />
                            </div>
                          </div>
                        ))
                      )}
                      <p className="text-[10px] text-slate-300 dark:text-slate-500">{sixDimTestResult.note}</p>
                    </div>
                  )}

                  {/* 后台强制刷新结果 */}
                  {sixDimRefreshResult && (
                    <div className="mt-3 p-2.5 rounded-md border text-xs bg-green-50 dark:bg-green-900/20 border-green-200 dark:border-green-800">
                      <div className="flex items-center gap-1.5 font-medium text-green-700 dark:text-green-400">
                        <CheckCircle className="w-3.5 h-3.5" />
                        {sixDimRefreshResult.error ? '刷新失败' : '后台强制刷新完成'}
                      </div>
                      {sixDimRefreshResult.error ? (
                        <p className="mt-1 text-[11px] text-red-600 dark:text-red-400">{sixDimRefreshResult.error}</p>
                      ) : (
                        <div className="mt-1.5 space-y-0.5 text-[11px] text-green-700 dark:text-green-400">
                          <div>{sixDimRefreshResult.as_of} 总分={sixDimRefreshResult.raw_total_score?.toFixed(1)}（冲突调整 {sixDimRefreshResult.adjusted_total_score?.toFixed(1)}，冲突数 {sixDimRefreshResult.conflict_count}）</div>
                          <div>仓位系数 <span className="font-medium">{sixDimRefreshResult.position_rate?.toFixed(2)}</span> · 市场标签 <span className="font-medium">{sixDimRefreshResult.market_tag}</span></div>
                        </div>
                      )}
                    </div>
                  )}
                </div>
              </div>
              )}

              {thsOpen && (
              <div className="space-y-3 p-3 bg-slate-50 dark:bg-slate-800/50 rounded-md">
                <div>
                  <h3 className="text-xs font-medium text-slate-700 dark:text-slate-300 flex items-center gap-1.5">
                    <Activity className="w-3.5 h-3.5" />
                    同花顺官方数据服务配置
                  </h3>
                  <p className="text-xs text-slate-500 dark:text-slate-400 mt-1">
                    需在<a href="https://fuyao.aicubes.cn" target="_blank" rel="noreferrer" className="text-brand-600 dark:text-brand-400 underline">https://fuyao.aicubes.cn</a>金融数据服务开通后填写 API Key。保存后无需重启。
                  </p>
                </div>
                <div className="flex items-center justify-between gap-2">
                  <label className="flex items-center gap-1.5 text-xs text-slate-600 dark:text-slate-300">
                    <input
                      type="checkbox"
                      checked={thsEnabled}
                      onChange={(e) => setThsEnabled(e.target.checked)}
                      className="w-3.5 h-3.5 rounded border-slate-300 dark:border-slate-600 accent-brand-600"
                    />
                    启用官方数据源
                  </label>
                </div>
                <div className="space-y-2">
                  <label className="block text-xs text-slate-500 dark:text-slate-400">
                    API Key（X-api-key）
                    <input
                      type="password"
                      value={thsApiKey}
                      onChange={(e) => setThsApiKey(e.target.value)}
                      placeholder="填写官方开通的 API Key"
                      className="mt-1 w-full px-2.5 py-1.5 rounded-md text-xs border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-800 text-slate-700 dark:text-slate-300 focus:outline-none focus:ring-1 focus:ring-brand-500"
                    />
                  </label>
                  <label className="block text-xs text-slate-500 dark:text-slate-400">
                    服务地址（可选，默认官方 https://fuyao.aicubes.cn）
                    <input
                      type="text"
                      value={thsBaseUrl}
                      onChange={(e) => setThsBaseUrl(e.target.value)}
                      placeholder="https://fuyao.aicubes.cn"
                      className="mt-1 w-full px-2.5 py-1.5 rounded-md text-xs border border-slate-300 dark:border-slate-600 bg-white dark:bg-slate-800 text-slate-700 dark:text-slate-300 focus:outline-none focus:ring-1 focus:ring-brand-500"
                    />
                  </label>
                </div>
                <p className="text-[10px] text-slate-400 dark:text-slate-500">
                  启用后：六维判势情绪维度优先取官方涨停/跌停/炸板池+连板天梯（真实炸板率）；「财务数据维护」可选择同花顺官方源；数据维护区可一键导入全市场复权因子；盘中可用估值/集合竞价快照。
                </p>
                <div className="flex items-center gap-2">
                  <button
                    onClick={handleSaveTHS}
                    className="px-2.5 py-1.5 rounded-md text-xs font-medium bg-brand-600 hover:bg-brand-700 text-white transition-colors flex items-center gap-1.5"
                  >
                    <Save className="w-3.5 h-3.5" />
                    保存同花顺配置
                  </button>
                  {thsSaved && <span className="text-[11px] text-green-600 dark:text-green-400">已保存，立即生效</span>}
                </div>
              </div>
              )}

              {/* 辅助数据源：保存 */}
              <div className="pt-3 border-t border-slate-200 dark:border-slate-700">
                <div className="flex gap-2">
                  <button
                    onClick={handleSave}
                    className="btn-primary"
                  >
                    {t('settings.save')}
                  </button>
                </div>

                {saveError && (
                  <div className="mt-2 p-2.5 rounded-md text-xs bg-red-50 dark:bg-red-900/20 text-red-700 dark:text-red-400 border border-red-200 dark:border-red-800">
                    <span className="font-medium">❌ 保存失败：</span>
                    <span className="ml-1">{saveError}</span>
                  </div>
                )}
                {saveSuccess && (
                  <div className="mt-2 p-2.5 rounded-md text-xs bg-green-50 dark:bg-green-900/20 text-green-700 dark:text-green-400 border border-green-200 dark:border-green-800">
                    <span className="font-medium">✅ 设置已保存</span>
                  </div>
                )}
              </div>
              </>
              )}
            </div>
          )}

          {activeTab === 'audit' && (
            <div className="space-y-4">
              <h2 className="text-base font-semibold text-slate-800 dark:text-slate-100">{t('settings.auditLog')}</h2>
              <p className="text-xs text-slate-500 dark:text-slate-400">
                {t('settings.auditLogDesc')}
              </p>
              <div className="border-t border-slate-200 dark:border-slate-700 pt-3">
                <AuditLogPage />
              </div>
            </div>
          )}

          {activeTab === 'maintenance' && (
            <div className="space-y-5">
              <h2 className="text-base font-semibold text-slate-800 dark:text-slate-100">数据维护</h2>

              {/* 数据维护子页签：同花顺数据 / 通达信数据 / 系统初始化 / 日志数据清理 */}
              <div className="flex flex-wrap gap-2 border-b border-slate-200 dark:border-slate-700 pb-2">
                {([
                  ['ths', '同花顺数据'],
                  ['tdx', '通达信数据'],
                  ['sysinit', '系统初始化'],
                  ['log', '日志数据清理'],
                ] as const).map(([k, label]) => (
                  <button
                    key={k}
                    onClick={() => setMaintTab(k)}
                    className={`px-3 py-1.5 rounded-md text-xs font-medium transition-colors ${
                      maintTab === k
                        ? 'bg-brand-600 text-white'
                        : 'text-slate-600 dark:text-slate-400 hover:bg-slate-100 dark:hover:bg-slate-800'
                    }`}
                  >
                    {label}
                  </button>
                ))}
              </div>

              {/* ===== 页签1：通达信数据（股票时序 + 财务） ===== */}
              {maintTab === 'tdx' && (
              <>
              {/* 1. 股票时序数据维护 */}
              <div className="p-4 rounded-lg bg-slate-50 dark:bg-slate-800/50 space-y-3">
                <div className="flex items-center gap-2">
                  <HardDrive className="w-4 h-4 text-brand-600 dark:text-brand-400" />
                  <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">股票时序数据维护</h3>
                </div>
                <p className="text-xs text-slate-500 dark:text-slate-400">
                  从通达信 vipdoc 文件夹中的 .day 日线文件同步数据到 stock.duckdb 数据库。同步过程会重建 ohlc 行情表，耗时取决于数据量。
                </p>
                <div>
                  <label className="block text-xs font-medium text-slate-600 dark:text-slate-400 mb-1.5">TDX 数据路径</label>
                  <div className="flex gap-2">
                    <input
                      type="text"
                      value={syncTdxPath}
                      onChange={(e) => setSyncTdxPath(e.target.value)}
                      className="input-field"
                      placeholder="D:\tdx"
                    />
                    <button
                      onClick={handleStartSync}
                      disabled={syncStatus?.running}
                      className="px-3 py-1.5 rounded-md text-xs font-medium bg-brand-600 hover:bg-brand-700 text-white transition-colors flex items-center gap-1.5 disabled:opacity-50 shrink-0"
                    >
                      {syncStatus?.running ? <RefreshCw className="w-3.5 h-3.5 animate-spin" /> : <Download className="w-3.5 h-3.5" />}
                      {syncStatus?.running ? '同步中...' : '开始同步'}
                    </button>
                  </div>
                </div>

                {syncStatus && (
                  <div className="text-xs text-slate-600 dark:text-slate-400 space-y-1">
                    {syncStatus.message && <p>状态：{syncStatus.message}</p>}
                    {(syncStatus.total_files ?? 0) > 0 && (
                      <p>
                        进度：{syncStatus.processed_files || 0} / {syncStatus.total_files} 个文件
                        {syncStatus.inserted_rows ? `，已写入 ${syncStatus.inserted_rows.toLocaleString()} 行` : ''}
                      </p>
                    )}
                    {syncStatus.running && (syncStatus.total_files ?? 0) > 0 && (
                      <div className="w-full h-1.5 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
                        <div
                          className="h-full bg-brand-500 transition-all"
                          style={{ width: `${Math.min(100, Math.round(((syncStatus.processed_files || 0) / syncStatus.total_files) * 100))}%` }}
                        />
                      </div>
                    )}
                    {syncStatus.error && <p className="text-red-600 dark:text-red-400">错误：{syncStatus.error}</p>}
                    {syncStatus.done && !syncStatus.error && (
                      <p className="text-green-600 dark:text-green-400">
                        同步完成：{syncStatus.inserted_rows?.toLocaleString()} 行数据已写入（耗时 {syncStatus.duration_sec?.toFixed(1)} 秒）
                      </p>
                    )}
                  </div>
                )}
              </div>

              {/* 2. 财务数据维护（通达信终端，本地 RPC 直连，不封 IP） */}
              <div className="p-4 rounded-lg bg-slate-50 dark:bg-slate-800/50 space-y-3">
                <div className="flex items-center gap-2">
                  <BarChart3 className="w-4 h-4 text-brand-600 dark:text-brand-400" />
                  <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">财务数据维护</h3>
                  <span className="text-[10px] px-1.5 py-0.5 rounded bg-brand-100 dark:bg-brand-900/40 text-brand-700 dark:text-brand-300">
                    通达信终端
                  </span>
                </div>
                <p className="text-xs text-slate-500 dark:text-slate-400">
                  直接从通达信终端同步各股票的历史财务数据（营收、净利润、ROE、毛利率、资产负债率、股本等）到 stock.duckdb 的
                  <code className="mx-1 px-1 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-[11px]">financial_report</code>表。
                  数据按「报告期」存储并记录披露日，回测时仅用当时已披露的数据，杜绝未来函数。
                  同花顺官方财务数据请到「同花顺数据」页签同步。
                </p>
                <div className="flex gap-2">
                  <button
                    onClick={() => handleStartFinancialSync('incremental', 'tdx')}
                    disabled={finSyncStatus?.running}
                    className="px-3 py-1.5 rounded-md text-xs font-medium bg-brand-600 hover:bg-brand-700 text-white transition-colors flex items-center gap-1.5 disabled:opacity-50"
                  >
                    {finSyncStatus?.running ? <RefreshCw className="w-3.5 h-3.5 animate-spin" /> : <Download className="w-3.5 h-3.5" />}
                    {finSyncStatus?.running ? '同步中...' : '增量更新'}
                  </button>
                  <button
                    onClick={() => handleStartFinancialSync('full', 'tdx')}
                    disabled={finSyncStatus?.running}
                    className="px-3 py-1.5 rounded-md text-xs font-medium border border-slate-300 dark:border-slate-600 text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 transition-colors flex items-center gap-1.5 disabled:opacity-50"
                  >
                    <Database className="w-3.5 h-3.5" />
                    全量重建
                  </button>
                </div>
                <p className="text-[10px] text-slate-400 dark:text-slate-500">
                  数据源固定为「通达信终端」（本地 RPC 直连，不封 IP），需主程序已登录并下载专业财务数据。全量重建遍历全部 A 股拉取全历史，耗时较长；日常建议用「增量更新」。
                </p>

                {finSyncStatus && (
                  <div className="text-xs text-slate-600 dark:text-slate-400 space-y-1">
                    {finSyncStatus.message && <p>状态：{finSyncStatus.message}</p>}
                    {finSyncStatus.mode && <p>模式：{finSyncStatus.mode === 'full' ? '全量' : '增量'}（通达信终端）</p>}
                    {(finSyncStatus.total_stocks ?? 0) > 0 && (
                      <p>
                        进度：{finSyncStatus.processed_stocks || 0} / {finSyncStatus.total_stocks} 只股票，
                        写入 {finSyncStatus.inserted_records?.toLocaleString() || 0} 条
                        {finSyncStatus.failed_count ? `，失败 ${finSyncStatus.failed_count}` : ''}
                      </p>
                    )}
                    {finSyncStatus.running && (finSyncStatus.total_stocks ?? 0) > 0 && (
                      <div className="w-full h-1.5 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
                        <div
                          className="h-full bg-brand-500 transition-all"
                          style={{ width: `${Math.min(100, Math.round(((finSyncStatus.processed_stocks || 0) / finSyncStatus.total_stocks) * 100))}%` }}
                        />
                      </div>
                    )}
                    {finSyncStatus.error && <p className="text-red-600 dark:text-red-400">错误：{finSyncStatus.error}</p>}
                    {finSyncStatus.last_result && !finSyncStatus.running && (
                      <p className="text-green-600 dark:text-green-400">上次结果：{finSyncStatus.last_result}（{finSyncStatus.last_update || ''}）</p>
                    )}
                  </div>
                )}
              </div>
              </>
              )}

              {/* ===== 页签2：同花顺数据（复权因子导入 + 日K导入 + 估值/集合竞价） ===== */}
              {maintTab === 'ths' && (
              <>

              {/* 3. 同花顺复权因子导入 + 前复权视图 */}
              <div className="p-4 rounded-lg bg-slate-50 dark:bg-slate-800/50 space-y-3">
                <div className="flex items-center gap-2">
                  <Activity className="w-4 h-4 text-brand-600 dark:text-brand-400" />
                  <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">复权因子导入（前复权视图）</h3>
                  <span className="text-[10px] px-1.5 py-0.5 rounded bg-brand-100 dark:bg-brand-900/40 text-brand-700 dark:text-brand-300">
                    {adjStatus?.configured ? '官方源已配置' : '未配置官方源'}
                  </span>
                </div>
                <p className="text-xs text-slate-500 dark:text-slate-400">
                  从同花顺官方下载全市场复权事件（分红/送股/配股）Parquet，结合本机真实日K推算日频复权因子，
                  构建 <code className="mx-1 px-1 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-[11px]">stock.adj_factor</code> 表
                  与 <code className="mx-1 px-1 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-[11px]">stock.ohlc_qfq</code> 前复权视图。
                  最新收盘为基准（前复权因子=1），历史价格按因子缩放。
                </p>
                <div className="flex flex-wrap gap-2 items-center">
                  <button
                    onClick={handleSyncAll}
                    disabled={syncAllBusy || !adjStatus?.configured}
                    className="px-3 py-1.5 rounded-md text-xs font-semibold bg-brand-600 hover:bg-brand-700 text-white transition-colors flex items-center gap-1.5 disabled:opacity-50"
                  >
                    {syncAllBusy ? <RefreshCw className="w-3.5 h-3.5 animate-spin" /> : <Calendar className="w-3.5 h-3.5" />}
                    {syncAllBusy ? '一键同步中（复权因子+交易日历）...' : '一键同步全部（复权因子 + 交易日历）'}
                  </button>
                  <button
                    onClick={handleImportAdjFactors}
                    disabled={adjBusy || syncAllBusy || !adjStatus?.configured}
                    className="px-3 py-1.5 rounded-md text-xs font-medium border border-slate-300 dark:border-slate-600 text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 transition-colors flex items-center gap-1.5"
                  >
                    {adjBusy ? <RefreshCw className="w-3.5 h-3.5 animate-spin" /> : <Download className="w-3.5 h-3.5" />}
                    {adjBusy ? '仅导入复权因子...' : '仅导入复权因子'}
                  </button>
                  {adjStatus?.factor_symbols > 0 && (
                    <span className="text-[11px] text-slate-500 dark:text-slate-400">
                      已导入 {adjStatus?.event_rows?.toLocaleString() || 0} 条事件，覆盖 {adjStatus?.factor_symbols?.toLocaleString() || 0} 只股票
                      {adjStatus?.view_ready ? '，前复权视图已就绪' : ''}
                    </span>
                  )}
                </div>
                {calStatus?.has_data && (
                  <p className="text-[11px] text-slate-500 dark:text-slate-400">
                    交易日历：{calStatus?.count?.toLocaleString() || 0} 个交易日，{calStatus?.date_min} ~ {calStatus?.date_max}
                    {calStatus?.configured ? '' : '（官方源未配置，不会自动同步）'}
                  </p>
                )}
                {adjErr && <p className="text-xs text-red-600 dark:text-red-400">错误：{adjErr}</p>}
                {calErr && <p className="text-xs text-red-600 dark:text-red-400">交易日历错误：{calErr}</p>}
                {adjResult && (
                  <div className="text-xs text-green-600 dark:text-green-400 space-y-0.5">
                    {(adjResult as any)._steps
                      ? (adjResult as any)._steps.map((s: string, i: number) => <p key={i}>{s}</p>)
                      : <p>导入完成：{adjResult.message}</p>}
                  </div>
                )}
              </div>

              {/* 4. 同花顺全市场日K导入（stock.ohlc 底层 stock_daily） */}
              <div className="p-4 rounded-lg bg-slate-50 dark:bg-slate-800/50 space-y-3">
                <div className="flex items-center gap-2">
                  <BarChart3 className="w-4 h-4 text-brand-600 dark:text-brand-400" />
                  <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">全市场日K导入（stock.ohlc）</h3>
                  <span className="text-[10px] px-1.5 py-0.5 rounded bg-brand-100 dark:bg-brand-900/40 text-brand-700 dark:text-brand-300">
                    {dailyKStatus?.configured ? '官方源已配置' : '未配置官方源'}
                  </span>
                </div>
                <p className="text-xs text-slate-500 dark:text-slate-400">
                  从同花顺官方下载全市场日K Parquet（原始未复权），按 (symbol,date) 去重后增量合并到
                  <code className="mx-1 px-1 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-[11px]">stock.stock_daily</code>
                  （<code className="mx-1 px-1 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-[11px]">stock.ohlc</code> 视图底层）。
                  已存在数据自动跳过，可重复导入。
                </p>
                <div className="flex flex-wrap gap-2 items-center">
                  <button
                    onClick={() => handleImportDailyK('incr')}
                    disabled={dailyKBusy || !dailyKStatus?.configured}
                    className="px-3 py-1.5 rounded-md text-xs font-medium bg-brand-600 hover:bg-brand-700 text-white transition-colors flex items-center gap-1.5 disabled:opacity-50"
                  >
                    {dailyKBusy ? <RefreshCw className="w-3.5 h-3.5 animate-spin" /> : <Download className="w-3.5 h-3.5" />}
                    {dailyKBusy ? '导入中...' : '增量导入（近10交易日）'}
                  </button>
                  <button
                    onClick={() => handleImportDailyK('full')}
                    disabled={dailyKBusy || !dailyKStatus?.configured}
                    className="px-3 py-1.5 rounded-md text-xs font-medium bg-brand-600 hover:bg-brand-700 text-white transition-colors flex items-center gap-1.5 disabled:opacity-50"
                  >
                    <Download className="w-3.5 h-3.5" />
                    全量导入（近3年）
                  </button>
                  {dailyKStatus?.has_data && (
                    <span className="text-[11px] text-slate-500 dark:text-slate-400">
                      当前 {dailyKStatus?.rows?.toLocaleString() || 0} 行，覆盖 {dailyKStatus?.symbols?.toLocaleString() || 0} 只股票
                      {dailyKStatus?.latest_date ? `，最新 ${dailyKStatus.latest_date}` : ''}
                    </span>
                  )}
                </div>
                {dailyKErr && <p className="text-xs text-red-600 dark:text-red-400">错误：{dailyKErr}</p>}
                {dailyKResult && (
                  <p className="text-xs text-green-600 dark:text-green-400">完成：{dailyKResult.message}</p>
                )}
              </div>

              {/* 7. 同花顺「财务数据」同步导入 */}
              <div className="p-4 rounded-lg bg-slate-50 dark:bg-slate-800/50 space-y-3">
                <div className="flex items-center gap-2">
                  <BarChart3 className="w-4 h-4 text-brand-600 dark:text-brand-400" />
                  <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">财务数据维护</h3>
                  <span className="text-[10px] px-1.5 py-0.5 rounded bg-brand-100 dark:bg-brand-900/40 text-brand-700 dark:text-brand-300">
                    同花顺官方（需配置上方 API Key）
                  </span>
                </div>
                <p className="text-xs text-slate-500 dark:text-slate-400">
                  从同花顺官方同步各股票历史财务数据（营收、净利润、ROE、毛利率、资产负债率、股本等）到
                  <code className="mx-1 px-1 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-[11px]">financial_report</code>表。
                  数据按「报告期」存储并记录披露日，回测时仅用当时已披露的数据，杜绝未来函数。
                </p>
                <div className="flex gap-2 items-center">
                  <button
                    onClick={() => handleStartTHSFinancialSync('incremental')}
                    disabled={thsFinSync?.running}
                    className="px-3 py-1.5 rounded-md text-xs font-medium bg-brand-600 hover:bg-brand-700 text-white transition-colors flex items-center gap-1.5 disabled:opacity-50"
                  >
                    {thsFinSync?.running ? <RefreshCw className="w-3.5 h-3.5 animate-spin" /> : <Download className="w-3.5 h-3.5" />}
                    {thsFinSync?.running ? '同步中...' : '增量更新'}
                  </button>
                  <button
                    onClick={() => handleStartTHSFinancialSync('full')}
                    disabled={thsFinSync?.running}
                    className="px-3 py-1.5 rounded-md text-xs font-medium border border-slate-300 dark:border-slate-600 text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 transition-colors flex items-center gap-1.5 disabled:opacity-50"
                  >
                    <Database className="w-3.5 h-3.5" />
                    全量重建
                  </button>
                </div>
                <p className="text-[10px] text-slate-400 dark:text-slate-500">
                  提示：全量重建遍历全部 A 股拉取全历史，耗时较长；日常建议用「增量更新」只补新报告期。未配置官方 API Key 时将被拒绝。
                </p>
                {thsFinSync && (
                  <div className="text-xs text-slate-600 dark:text-slate-400 space-y-1">
                    {thsFinSync.message && <p>状态：{thsFinSync.message}</p>}
                    {thsFinSync.mode && <p>模式：{thsFinSync.mode === 'full' ? '全量' : '增量'}（同花顺官方）</p>}
                    {(thsFinSync.total_stocks ?? 0) > 0 && (
                      <p>
                        进度：{thsFinSync.processed_stocks || 0} / {thsFinSync.total_stocks} 只股票，
                        写入 {thsFinSync.inserted_records?.toLocaleString() || 0} 条
                        {thsFinSync.failed_count ? `，失败 ${thsFinSync.failed_count}` : ''}
                      </p>
                    )}
                    {thsFinSync.running && (thsFinSync.total_stocks ?? 0) > 0 && (
                      <div className="w-full h-1.5 bg-slate-200 dark:bg-slate-700 rounded-full overflow-hidden">
                        <div
                          className="h-full bg-brand-500 transition-all"
                          style={{ width: `${Math.min(100, Math.round(((thsFinSync.processed_stocks || 0) / thsFinSync.total_stocks) * 100))}%` }}
                        />
                      </div>
                    )}
                    {thsFinSync.error && <p className="text-red-600 dark:text-red-400">错误：{thsFinSync.error}</p>}
                    {thsFinSync.last_result && !thsFinSync.running && (
                      <p className="text-green-600 dark:text-green-400">上次结果：{thsFinSync.last_result}（{thsFinSync.last_update || ''}）</p>
                    )}
                  </div>
                )}
              </div>
              </>
              )}

              {/* ===== 页签3：系统初始化 ===== */}
              {maintTab === 'sysinit' && (
              <>

              {/* 5. 系统初始化 */}
              <div className="p-4 rounded-lg bg-red-50 dark:bg-red-900/10 border border-red-200 dark:border-red-800/50 space-y-3">
                <div className="flex items-center gap-2">
                  <AlertTriangle className="w-4 h-4 text-red-600 dark:text-red-400" />
                  <h3 className="text-sm font-semibold text-red-700 dark:text-red-400">系统初始化</h3>
                </div>
                <p className="text-xs text-red-600/90 dark:text-red-400/90">
                  <strong>危险操作：</strong>将清空所有用户过程数据（交易记录、持仓、策略、回测、投资计划、智能体记录、复盘、因子质量、仓位配置、审计日志等），
                  <strong>仅保留字典数据</strong>（股票标的、市场指数、自选股、系统设置）与表结构。此操作不可恢复，建议先备份！
                </p>
                <div className="flex gap-2 items-center">
                  <input
                    type="text"
                    value={resetConfirm}
                    onChange={(e) => setResetConfirm(e.target.value)}
                    className="input-field flex-1"
                    placeholder='请输入 "RESET" 以确认初始化'
                  />
                  <button
                    onClick={handleReset}
                    disabled={resetBusy || resetConfirm.trim() !== 'RESET'}
                    className="px-3 py-1.5 rounded-md text-xs font-medium bg-red-600 hover:bg-red-700 text-white transition-colors flex items-center gap-1.5 disabled:opacity-50 shrink-0"
                  >
                    <Trash2 className="w-3.5 h-3.5" />
                    {resetBusy ? '初始化中...' : '执行系统初始化'}
                  </button>
                </div>
                {resetResult && (
                  <div className={`text-xs rounded p-2 ${
                    resetResult.error
                      ? 'bg-red-100 dark:bg-red-900/30 text-red-700 dark:text-red-400'
                      : 'bg-green-100 dark:bg-green-900/30 text-green-700 dark:text-green-400'
                  }`}>
                    {resetResult.error || '系统初始化完成，用户数据已全部清空。请重启应用使设置生效。'}
                  </div>
                )}
              </div>
              </>
              )}

              {/* ===== 页签4：日志数据清理 ===== */}
              {maintTab === 'log' && (
              <>

              {/* 3. 基础数据维护 */}
              <div className="p-4 rounded-lg bg-slate-50 dark:bg-slate-800/50 space-y-3">
                <div className="flex items-center gap-2">
                  <Trash2 className="w-4 h-4 text-slate-600 dark:text-slate-400" />
                  <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">基础数据维护</h3>
                </div>
                <p className="text-xs text-slate-500 dark:text-slate-400">
                  清理半年以上的日志文件与审计数据，释放磁盘空间。
                </p>
                <button
                  onClick={handleCleanup}
                  disabled={cleanupBusy}
                  className="px-3 py-1.5 rounded-md text-xs font-medium border border-slate-300 dark:border-slate-600 text-slate-700 dark:text-slate-300 hover:bg-slate-100 dark:hover:bg-slate-700 transition-colors flex items-center gap-1.5 disabled:opacity-50"
                >
                  {cleanupBusy ? <RefreshCw className="w-3.5 h-3.5 animate-spin" /> : <Trash2 className="w-3.5 h-3.5" />}
                  {cleanupBusy ? '清理中...' : '开始清理'}
                </button>
                {cleanupResult && (
                  <div className={`text-xs rounded p-2 ${
                    cleanupResult.error
                      ? 'bg-red-100 dark:bg-red-900/30 text-red-700 dark:text-red-400'
                      : 'bg-green-100 dark:bg-green-900/30 text-green-700 dark:text-green-400'
                  }`}>
                    {cleanupResult.error ? (
                      cleanupResult.error
                    ) : (
                      <span>
                        清理完成：删除日志文件 {cleanupResult.result?.deleted_log_files || 0} 个，
                        审计数据 {cleanupResult.result?.deleted_audit_rows || 0} 条
                        （截止 {cleanupResult.result?.cutoff_date || ''}）
                      </span>
                    )}
                  </div>
                )}
              </div>
              </>
              )}
            </div>
          )}

          {activeTab === 'health' && (
            <div className="space-y-5">
              <div className="flex items-center justify-between">
                <div>
                  <h2 className="text-base font-semibold text-slate-800 dark:text-slate-100 flex items-center gap-2">
                    <HeartPulse className="w-4 h-4 text-brand-500" />
                    系统健康度
                  </h2>
                  <p className="text-xs text-slate-500 dark:text-slate-400 mt-1">
                    检查数据源、数据库、AI服务、Agent、Portfolio、Scheduler、Trading 各组件状态
                  </p>
                </div>
                <div className="flex items-center gap-2">
                  {health?.checkedAt && (
                    <span className="text-xs text-slate-400">
                      上次检查 {health.checkedAt}
                      {typeof health.total_ms === 'number' ? `（耗时 ${health.total_ms}ms）` : health.duration ? `（耗时 ${health.duration} 秒）` : ''}
                    </span>
                  )}
                  <button
                    onClick={loadHealth}
                    disabled={healthLoading}
                    className="px-3 py-1.5 rounded-md text-xs font-medium bg-brand-500 hover:bg-brand-600 text-white transition-colors flex items-center gap-1.5 disabled:opacity-50"
                  >
                    <RefreshCw className={`w-3.5 h-3.5 ${healthLoading ? 'animate-spin' : ''}`} />
                    {healthLoading ? '检查中...' : '重新检查'}
                  </button>
                </div>
              </div>

              {healthError && (
                <div className="p-3 rounded-lg bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800 text-xs text-red-700 dark:text-red-300">
                  获取系统健康度失败：{healthError}
                </div>
              )}

              {health?.items?.length > 0 && (
                <div className="space-y-2">
                  {health.items.map((item: any) => (
                    <div
                      key={item.key}
                      className={`flex items-center justify-between p-3 rounded-lg border ${
                        item.ok
                          ? 'bg-green-50 dark:bg-green-900/10 border-green-200 dark:border-green-800'
                          : item.warning
                            ? 'bg-amber-50 dark:bg-amber-900/10 border-amber-200 dark:border-amber-800'
                            : 'bg-red-50 dark:bg-red-900/10 border-red-200 dark:border-red-800'
                      }`}
                    >
                      <div className="flex items-center gap-3">
                        <span className={`w-2.5 h-2.5 rounded-full ${
                          item.ok ? 'bg-green-500' : item.warning ? 'bg-amber-500' : 'bg-red-500'
                        }`} />
                        <span className="text-sm font-medium text-slate-700 dark:text-slate-300">{item.name}</span>
                      </div>
                      <div className="flex items-center gap-2">
                        <span className="text-xs text-slate-500 dark:text-slate-400">{item.detail}</span>
                        {typeof item.duration_ms === 'number' && (
                          <span className="text-[10px] text-slate-400 dark:text-slate-500 font-mono">
                            {item.duration_ms}ms
                          </span>
                        )}
                        <span className={`text-xs font-semibold ${
                          item.ok ? 'text-green-600 dark:text-green-400' : item.warning ? 'text-amber-600 dark:text-amber-400' : 'text-red-600 dark:text-red-400'
                        }`}>
                          {item.ok ? '✓' : item.warning ? '⚠' : '✗'}
                        </span>
                      </div>
                    </div>
                  ))}
                </div>
              )}

              {health && !health.all_ok && (
                <div className="p-3 rounded-lg bg-amber-50 dark:bg-amber-900/10 border border-amber-200 dark:border-amber-800 text-xs text-amber-700 dark:text-amber-300">
                  部分组件异常，请根据上方状态排查。若 AI 服务不可用，请检查 API Key 配置。
                </div>
              )}
            </div>
          )}

          {activeTab === 'about' && (
            <div className="space-y-5">
              {/* 应用信息 */}
              <div className="text-center py-4">
                <div className="mx-auto mb-3 w-16 h-16">
                  <AppLogo size={64} rounded="rounded-xl" />
                </div>
                <h2 className="text-lg font-bold text-slate-800 dark:text-slate-100">
                  {systemInfo?.appName || 'QuantBot AI'}
                </h2>
                <p className="text-xs text-slate-500 dark:text-slate-400 mt-1">
                  版本 {systemInfo?.appVersion || '1.5.0'}
                </p>
              </div>

              {/* 关于文本 */}
              {systemInfo?.aboutText && (
                <div className="p-4 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <pre className="whitespace-pre-wrap text-xs text-slate-600 dark:text-slate-400 leading-relaxed font-sans">
{systemInfo.aboutText}
                  </pre>
                </div>
              )}

              {/* 软件更新 */}
              <div className="p-4 rounded-lg border border-slate-200 dark:border-slate-700">
                <div className="flex items-center gap-2 mb-3">
                  <Rocket className="w-4 h-4 text-blue-500" />
                  <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">{t('settings.updateSection')}</h3>
                </div>

                {updateState === 'idle' && (
                  <div className="space-y-3">
                    {updateInfo?.current_version && (
                      <p className="text-xs text-slate-500 dark:text-slate-400">
                        {t('settings.upToDate').replace('{version}', updateInfo.current_version)}
                      </p>
                    )}
                    <button
                      onClick={handleCheckUpdate}
                      className="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-blue-500 text-white hover:bg-blue-600 transition-colors"
                    >
                      <RefreshCw className="w-3.5 h-3.5" />
                      {t('settings.checkUpdate')}
                    </button>
                  </div>
                )}

                {updateState === 'checking' && (
                  <div className="flex items-center gap-2 text-xs text-slate-500 dark:text-slate-400">
                    <Loader2 className="w-4 h-4 animate-spin" />
                    {t('settings.checkingUpdate')}
                  </div>
                )}

                {updateState === 'available' && (
                  <div className="space-y-3">
                    <div className="grid grid-cols-2 gap-2 text-xs">
                      <div className="text-slate-500 dark:text-slate-400">
                        {t('settings.currentVersion')}：
                        <span className="text-slate-700 dark:text-slate-200">{updateInfo?.current_version}</span>
                      </div>
                      <div className="text-slate-500 dark:text-slate-400">
                        {t('settings.latestVersion')}：
                        <span className="text-green-600 dark:text-green-400 font-medium">{updateInfo?.latest_version}</span>
                      </div>
                      <div className="col-span-2 text-slate-500 dark:text-slate-400">
                        {t('settings.packageSize')}：
                        <span className="text-slate-700 dark:text-slate-200">{formatBytes(updateInfo?.size)}</span>
                      </div>
                    </div>
                    {updateInfo?.changelog && (
                      <div>
                        <p className="text-xs font-medium text-slate-600 dark:text-slate-300 mb-1">{t('settings.updateChangelog')}</p>
                        <pre className="whitespace-pre-wrap text-[11px] text-slate-500 dark:text-slate-400 bg-slate-50 dark:bg-slate-800/50 rounded p-2 max-h-32 overflow-y-auto">{updateInfo.changelog}</pre>
                      </div>
                    )}
                    <button
                      onClick={handleDownloadUpdate}
                      className="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-blue-500 text-white hover:bg-blue-600 transition-colors"
                    >
                      <Download className="w-3.5 h-3.5" />
                      {t('settings.downloadUpdate')}
                    </button>
                  </div>
                )}

                {updateState === 'downloading' && (
                  <div className="space-y-2">
                    <div className="flex items-center justify-between text-xs text-slate-500 dark:text-slate-400">
                      <span className="inline-flex items-center gap-1.5">
                        <Loader2 className="w-4 h-4 animate-spin" />
                        {t('settings.downloadingUpdate')}
                      </span>
                      <span>{updateProgress}%</span>
                    </div>
                    <div className="h-2 rounded-full bg-slate-200 dark:bg-slate-700 overflow-hidden">
                      <div
                        className="h-full bg-blue-500 transition-all duration-300"
                        style={{ width: `${updateProgress}%` }}
                      />
                    </div>
                  </div>
                )}

                {updateState === 'ready' && (
                  <div className="space-y-3">
                    <p className="text-xs text-green-600 dark:text-green-400">{t('settings.updateReady')}</p>
                    <button
                      onClick={handleApplyUpdate}
                      className="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-green-500 text-white hover:bg-green-600 transition-colors"
                    >
                      <Rocket className="w-3.5 h-3.5" />
                      {t('settings.restartUpdate')}
                    </button>
                  </div>
                )}

                {updateState === 'error' && (
                  <div className="space-y-3">
                    <div className="flex items-center gap-1.5 text-xs text-red-500">
                      <AlertTriangle className="w-3.5 h-3.5" />
                      <span>{t('settings.updateFailed')}：{updateError}</span>
                    </div>
                    <button
                      onClick={handleCheckUpdate}
                      className="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-blue-500 text-white hover:bg-blue-600 transition-colors"
                    >
                      <RefreshCw className="w-3.5 h-3.5" />
                      {t('settings.checkUpdate')}
                    </button>
                  </div>
                )}
              </div>

              {/* 功能列表 */}
              {systemInfo?.features?.length > 0 && (
                <div>
                  <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300 mb-2">核心功能</h3>
                  <div className="grid grid-cols-2 gap-2">
                    {systemInfo.features.map((f: string, i: number) => (
                      <div key={i} className="flex items-center gap-2 p-2 rounded bg-slate-50 dark:bg-slate-800/50">
                        <CheckCircle className="w-4 h-4 text-green-500 shrink-0" />
                        <span className="text-xs text-slate-600 dark:text-slate-400">{f}</span>
                      </div>
                    ))}
                  </div>
                </div>
              )}

              {/* 技术信息 */}
              <div className="text-center pt-3 border-t border-slate-200 dark:border-slate-700">
                <p className="text-[10px] text-slate-400">
                  Powered by Go + React + SQLite + DuckDB
                </p>
                <p className="text-[10px] text-slate-400 mt-1">
                  © 2024-2026 QuantBot lab. All rights reserved.
                </p>
              </div>
            </div>
          )}

          {activeTab === 'contact' && (
            <div className="space-y-5">
              {/* 付费群 */}
              <div className="p-4 rounded-lg border border-slate-200 dark:border-slate-700">
                <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">💎 付费群（￥500 元）</h3>
                <p className="text-xs text-slate-500 dark:text-slate-400 mt-2 leading-relaxed">
                  付费咨询（入群费用：￥500 元）企鹅 Q 群活动。入群后，您可以获得：
                </p>
                <ul className="list-disc list-inside text-xs text-slate-500 dark:text-slate-400 mt-2 space-y-1">
                  <li>为大家搭建一个专业爱好者的圈子平台，随时交流</li>
                  <li>不定期在群里发布一些量化策略</li>
                  <li>QuantBot 的数据业务和技术问题答疑，可提供相应技术支持和建议</li>
                </ul>
                <div className="mt-3 p-3 rounded-lg bg-slate-50 dark:bg-slate-800/50">
                  <p className="text-xs font-medium text-slate-600 dark:text-slate-300 mb-1">加群步骤：</p>
                  <ol className="list-decimal list-inside text-xs text-slate-500 dark:text-slate-400 space-y-1">
                    <li>用微信扫码支付 <b>500</b>，备注：<b>QQ 号码和昵称</b></li>
                    <li>再用企鹅扫下方二维码加群，我们核对身份后通过</li>
                  </ol>
                </div>
                <div className="grid grid-cols-2 gap-3 mt-4">
                  <div className="text-center">
                    <p className="text-xs text-slate-500 dark:text-slate-400 mb-2">第一步：微信支付</p>
                    <img src={wxPayImg} alt="微信支付" className="mx-auto w-40 h-40 rounded-lg border border-slate-200 dark:border-slate-700 object-contain" />
                  </div>
                  <div className="text-center">
                    <p className="text-xs text-slate-500 dark:text-slate-400 mb-2">第二步：QQ VIP 群</p>
                    <img src={qqVipImg} alt="QQ VIP 群" className="mx-auto w-40 h-40 rounded-lg border border-slate-200 dark:border-slate-700 object-contain" />
                  </div>
                </div>
              </div>

              {/* 免费群 + 商务洽谈 并列排放 */}
              <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
                <div className="p-4 rounded-lg border border-slate-200 dark:border-slate-700">
                  <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">🆓 免费群</h3>
                  <p className="text-xs text-slate-500 dark:text-slate-400 mt-2 leading-relaxed">
                    如果您不想支付费用，也可以直接加我们的免费企鹅群。用企鹅直接扫二维码入群。
                  </p>
                  <div className="text-center mt-3">
                    <img src={qqFreeImg} alt="免费企鹅群" className="mx-auto w-40 h-40 rounded-lg border border-slate-200 dark:border-slate-700 object-contain" />
                  </div>
                </div>

                <div className="p-4 rounded-lg border border-slate-200 dark:border-slate-700">
                  <h3 className="text-sm font-semibold text-slate-700 dark:text-slate-300">💼 商务洽谈</h3>
                  <p className="text-xs text-slate-500 dark:text-slate-400 mt-2">请直接扫微信二维码：</p>
                  <div className="text-center mt-3">
                    <img src={wxBizImg} alt="商务洽谈" className="mx-auto w-40 h-40 rounded-lg border border-slate-200 dark:border-slate-700 object-contain" />
                  </div>
                </div>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

// 字节数格式化为可读文本
function formatBytes(bytes: number): string {
  if (!bytes || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0
  let val = bytes
  while (val >= 1024 && i < units.length - 1) {
    val /= 1024
    i++
  }
  return `${val.toFixed(1)} ${units[i]}`
}
