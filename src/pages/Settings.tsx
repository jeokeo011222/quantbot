import { useState, useEffect } from 'react'
import { Settings as SettingsIcon, Cpu, Database, Zap, Key, Save, TestTube, FileText, Info, CheckCircle, RefreshCw, AlertTriangle, Wrench, Trash2, HardDrive, Download, HeartPulse, ArrowRightLeft, FolderOpen, Brain, Globe } from 'lucide-react'
import { useI18nStore } from '../store/i18nStore'
import type { Language } from '../store/i18nStore'
import { useThemeStore } from '../store/themeStore'
import AuditLogPage from './AuditLog'
import LLMLogViewer from './LLMLogViewer'
import AppLogo from '../components/AppLogo'

type TabKey = 'general' | 'trading' | 'ai' | 'datasource' | 'llm' | 'audit' | 'maintenance' | 'health' | 'about'

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

  // 数据源测试状态
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<any>(null)

  // 保存错误提示
  const [saveError, setSaveError] = useState<string>('')
  const [saveSuccess, setSaveSuccess] = useState(false)

  // Native TDX 状态
  const [nativeTDXConnected, setNativeTDXConnected] = useState(false)
  const [nativeTDXConnecting, setNativeTDXConnecting] = useState(false)
  const [nativeTDXResult, setNativeTDXResult] = useState<{ success: boolean; message: string } | null>(null)

  // 系统信息状态
  const [systemInfo, setSystemInfo] = useState<any>(null)

  // 数据维护状态
  const [syncStatus, setSyncStatus] = useState<any>(null)
  const [syncTdxPath, setSyncTdxPath] = useState('')
  const [syncPolling, setSyncPolling] = useState(false)
  const [resetConfirm, setResetConfirm] = useState('')
  const [resetResult, setResetResult] = useState<any>(null)
  const [resetBusy, setResetBusy] = useState(false)
  const [cleanupResult, setCleanupResult] = useState<any>(null)
  const [cleanupBusy, setCleanupBusy] = useState(false)

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
        setInitialCapital(cfg.initial_capital || 100000)
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
    const timer = setInterval(async () => {
      try {
        const st = await callApp<any>('GetStockSyncStatus')
        setSyncStatus(st)
        if (!st?.running) {
          setSyncPolling(false)
        }
      } catch (e) {
        console.error('Failed to poll sync status:', e)
      }
    }, 1500)
    return () => clearInterval(timer)
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

  const tabs = [
    { key: 'general' as TabKey, icon: SettingsIcon, label: t('settings.general') },
    { key: 'trading' as TabKey, icon: ArrowRightLeft, label: t('settings.tradingInterface') },
    { key: 'ai' as TabKey, icon: Cpu, label: t('settings.aiProvider') },
    { key: 'datasource' as TabKey, icon: Database, label: t('settings.dataSource') },
    { key: 'llm' as TabKey, icon: Brain, label: 'LLM 监控' },
    { key: 'audit' as TabKey, icon: FileText, label: t('settings.auditLog') },
    { key: 'maintenance' as TabKey, icon: Wrench, label: '数据维护' },
    { key: 'health' as TabKey, icon: HeartPulse, label: '系统健康' },
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
                    onChange={(e) => setInitialCapital(Number(e.target.value))}
                    className="input-field"
                    min={10000}
                    step={10000}
                  />
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
                    💡 腾讯财经用于读取<b>实时行情</b>数据（组合持仓、看板、指数、交易报价等）。研究中心的选股引擎、策略、回测、ETF监控仍使用本地 DuckDB 数据，不受数据源切换影响。
                  </div>
                  <div className="text-xs text-slate-500 dark:text-slate-400">
                    无需安装通达信，也无需任何额外配置。保存后即可生效，可点击下方「测试连接」验证腾讯财经实时行情是否可用。
                  </div>
                </div>
              )}

              {/* 测试数据源 */}
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
            </div>
          )}

          {activeTab === 'llm' && (
            <LLMLogViewer />
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

              {/* 2. 系统初始化 */}
              <div className="p-4 rounded-lg bg-red-50 dark:bg-red-900/10 border border-red-200 dark:border-red-800/50 space-y-3">
                <div className="flex items-center gap-2">
                  <AlertTriangle className="w-4 h-4 text-red-600 dark:text-red-400" />
                  <h3 className="text-sm font-semibold text-red-700 dark:text-red-400">系统初始化</h3>
                </div>
                <p className="text-xs text-red-600/90 dark:text-red-400/90">
                  <strong>危险操作：</strong>将清空所有用户数据（交易记录、持仓、策略、回测结果、投资计划、智能体记录等），
                  仅保留系统基本信息、表结构与审计日志。此操作不可恢复！
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
                    {resetResult.error || '系统初始化完成，用户数据已全部清空。'}
                  </div>
                )}
              </div>

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
                  版本 {systemInfo?.appVersion || '1.0.0'}
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
        </div>
      </div>
    </div>
  )
}
