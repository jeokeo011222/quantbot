import { useState, useEffect, type ReactNode } from 'react'
import {
  Target,
  BarChart3,
  Briefcase,
  Settings,
  Cpu,
  Sparkles,
  Shield,
  Users,
  Activity,
  Eye,
  Wrench,
  Search,
  Radio,
} from 'lucide-react'
import { GetSystemInfo, GetMarketDataStatus } from '../../wailsjs/go/main/App'
import { useI18nStore } from '../store/i18nStore'
import { useToastStore } from '../store/toastStore'
import { useViewModeStore } from '../store/viewModeStore'
import ThemeToggle from './ThemeToggle'
import LanguageToggle from './LanguageToggle'
import TitleBar from './TitleBar'
import AppLogo from './AppLogo'

interface NavItem {
  path: string
  key: string
  icon: React.ComponentType<{ className?: string }>
}

const simpleNavItems: NavItem[] = [
  { path: '#/', key: 'nav.commandCenter', icon: Cpu },
  { path: '#/investment-planner', key: 'nav.investmentPlanner', icon: Sparkles },
  { path: '#/cio', key: 'nav.aiDecision', icon: Shield },
  { path: '#/activity', key: 'nav.activity', icon: Activity },
  { path: '#/portfolio', key: 'nav.myInvestments', icon: Briefcase },
  { path: '#/ai-team', key: 'nav.aiTeam', icon: Users },
]

const researchItems: NavItem[] = [
  { path: '#/screener', key: 'nav.screener', icon: Search },
  { path: '#/strategies', key: 'nav.strategies', icon: Target },
  { path: '#/backtest', key: 'nav.backtest', icon: BarChart3 },
  { path: '#/etf-monitor', key: 'nav.etfMonitor', icon: Radio },
]

interface LayoutProps {
  children?: ReactNode
}

export default function Layout({ children }: LayoutProps) {
  const t = useI18nStore((s) => s.t)
  const mode = useViewModeStore((s) => s.mode)
  const toggleMode = useViewModeStore((s) => s.toggleMode)
  const [currentPath, setCurrentPath] = useState(window.location.hash || '#/')
  const [appVersion, setAppVersion] = useState<string>('')
  const showWarning = useToastStore((s) => s.warning)

  useEffect(() => {
    // 从后端读取真实版本号（version.Version），避免侧边栏版本与 About 页/发布版本漂移
    GetSystemInfo()
      .then((info: any) => {
        if (info?.appVersion) setAppVersion(info.appVersion)
      })
      .catch(() => {
        // 忽略失败，回退到翻译中的默认版本文案
      })
  }, [])

  useEffect(() => {
    // 启动时检查行情数据新旧度：量化回测/选股依赖最新行情，过期则提示用户更新
    GetMarketDataStatus()
      .then((status: any) => {
        if (status?.needs_update) {
          showWarning(
            '行情数据需更新',
            `最新 ${status.latest_date}，应更新到 ${status.expected_date}（前一个交易日）。量化回测/选股依赖最新行情，请在「设置 → 数据更新」中同步行情数据。`,
          )
        }
      })
      .catch(() => {
        // 忽略失败（行情库未挂载等），由系统健康检查页展示详细状态
      })
  }, [])

  useEffect(() => {
    const handler = () => setCurrentPath(window.location.hash || '#/')
    window.addEventListener('hashchange', handler)
    return () => window.removeEventListener('hashchange', handler)
  }, [])

  const isDetailed = mode === 'detailed'

  return (
    <div className="flex flex-col h-full w-full bg-slate-50 dark:bg-slate-900 transition-colors overflow-hidden">
      <TitleBar title={t('app.name')} subtitle={t('app.subtitle')} disclaimer={t('app.disclaimer')} />

      <div className="flex flex-1 overflow-hidden min-h-0 h-0">
        {/* Sidebar */}
        <aside className="w-60 bg-white dark:bg-slate-800 border-r border-slate-200 dark:border-slate-700 flex flex-col transition-colors">
          {/* Logo */}
          <div className="px-5 py-4 border-b border-slate-200 dark:border-slate-700">
            <div className="flex items-center gap-3">
              <AppLogo size={36} rounded="rounded-xl" />
              <div>
                <h1 className="text-base font-bold text-slate-800 dark:text-slate-100 leading-tight">{t('app.name')}</h1>
                <p className="text-xs text-slate-500 dark:text-slate-400">{t('app.subtitle')}</p>
              </div>
            </div>
          </div>

          {/* Navigation */}
          <nav className="flex-1 px-2 py-3 space-y-0.5 overflow-y-auto">
            {/* Main nav - always visible */}
            {simpleNavItems.map((item) => (
              <NavLink key={item.path} item={item} currentPath={currentPath} t={t} />
            ))}

            {/* Detailed mode: Research Center */}
            {isDetailed && (
              <>
                <Divider />
                <SectionLabel>{t('nav.researchCenter')}</SectionLabel>
                {researchItems.map((item) => (
                  <NavLink key={item.path} item={item} currentPath={currentPath} t={t} />
                ))}
              </>
            )}

            {/* Divider before settings */}
            <Divider />
            <NavLink
              item={{ path: '#/settings', key: 'nav.settings', icon: Settings }}
              currentPath={currentPath}
              t={t}
            />
          </nav>

          {/* Footer */}
          <div className="px-4 py-4 border-t border-slate-200 dark:border-slate-700">
            <div className="flex items-center justify-between mb-3">
              <div className="flex items-center gap-1.5">
                <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
                <span className="text-xs text-slate-500 dark:text-slate-400">Local Runtime</span>
              </div>
              <div className="flex items-center gap-1">
                <LanguageToggle />
                <ThemeToggle />
              </div>
            </div>

            {/* Mode toggle */}
            <button
              onClick={toggleMode}
              className="w-full flex items-center justify-center gap-2 px-3 py-2 rounded-lg text-xs font-medium transition-colors bg-slate-100 dark:bg-slate-700 hover:bg-slate-200 dark:hover:bg-slate-600 text-slate-600 dark:text-slate-300"
            >
              {isDetailed ? <Wrench className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
              {isDetailed ? t('nav.simpleMode') : t('nav.detailedMode')}
            </button>

            <div className="mt-2 text-center text-xs text-slate-400">
              {appVersion ? `v${appVersion}` : t('app.version')}
            </div>
          </div>
        </aside>

        {/* Main Content */}
        <main className="flex-1 overflow-auto">
          <div className="p-6">
            {children}
          </div>
        </main>
      </div>
    </div>
  )
}

function NavLink({ item, currentPath, t }: { item: NavItem; currentPath: string; t: (key: string) => string }) {
  const isActive = currentPath === item.path
  return (
    <a
      href={item.path}
      className={`flex items-center gap-2 px-2.5 py-1.5 rounded-md text-xs font-medium transition-all duration-150 ${
        isActive
          ? 'bg-brand-50 text-brand-700 dark:bg-brand-900/30 dark:text-brand-300'
          : 'text-slate-600 hover:bg-slate-100 hover:text-slate-900 dark:text-slate-400 dark:hover:bg-slate-700 dark:hover:text-slate-100'
      }`}
    >
      <item.icon className="w-4 h-4" />
      {t(item.key)}
    </a>
  )
}

function Divider() {
  return <div className="my-2 border-t border-slate-200 dark:border-slate-700" />
}

function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <div className="px-3 pt-3 pb-1 text-xs font-semibold text-slate-400 dark:text-slate-500 uppercase tracking-wider">
      {children}
    </div>
  )
}
