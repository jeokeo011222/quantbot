import { Lock, Crown } from 'lucide-react'

interface VersionLockedProps {
  featureName: string
  requiredTier: 'pro' | 'enterprise'
  currentTier?: string
  onUpgrade?: () => void
  compact?: boolean
}

export function VersionLocked({
  featureName,
  requiredTier,
  currentTier,
  onUpgrade,
  compact = false,
}: VersionLockedProps) {
  const tierNames: Record<string, string> = {
    pro: 'Pro',
    enterprise: '企业版',
  }

  const tierColors: Record<string, string> = {
    pro: 'from-amber-500 to-orange-500',
    enterprise: 'from-purple-500 to-indigo-500',
  }

  if (compact) {
    return (
      <div className="inline-flex items-center gap-1 px-2 py-0.5 rounded bg-slate-100 dark:bg-slate-700 text-xs text-slate-500 dark:text-slate-400">
        <Lock className="w-3 h-3" />
        <span className={`bg-gradient-to-r ${tierColors[requiredTier]} bg-clip-text text-transparent font-medium`}>
          {tierNames[requiredTier]}
        </span>
      </div>
    )
  }

  return (
    <div className="p-4 bg-gradient-to-r from-slate-50 to-slate-100 dark:from-slate-800 dark:to-slate-700 rounded-xl border border-slate-200 dark:border-slate-600">
      <div className="flex items-start gap-3">
        <div className={`w-10 h-10 rounded-lg bg-gradient-to-br ${tierColors[requiredTier]} flex items-center justify-center shrink-0`}>
          <Lock className="w-5 h-5 text-white" />
        </div>
        <div className="flex-1">
          <div className="flex items-center gap-2 mb-1">
            <span className="font-medium text-slate-700 dark:text-slate-300">{featureName}</span>
            <span className={`inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded text-[10px] font-medium bg-gradient-to-r ${tierColors[requiredTier]} text-white`}>
              <Crown className="w-2.5 h-2.5" />
              {tierNames[requiredTier]}
            </span>
          </div>
          <p className="text-xs text-slate-500 dark:text-slate-400 mb-2">
            当前为 {currentTier || '免费版'}，升级到 {tierNames[requiredTier]} 即可使用此功能
          </p>
          {onUpgrade && (
            <button
              onClick={onUpgrade}
              className="text-xs font-medium text-indigo-600 dark:text-indigo-400 hover:underline"
            >
              立即升级 →
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

export function isFeatureLocked(
  currentTier: string | undefined,
  requiredTier: 'free' | 'pro' | 'enterprise'
): boolean {
  const tierLevels: Record<string, number> = {
    free: 0,
    pro: 1,
    enterprise: 2,
  }
  const current = tierLevels[currentTier || 'free'] || 0
  const required = tierLevels[requiredTier] || 0
  return current < required
}

export function getTierLimitations(tier: string | undefined): Record<string, boolean> {
  const t = tier || 'free'
  const isPro = t === 'pro' || t === 'enterprise'
  const isEnterprise = t === 'enterprise'
  return {
    allowAdvancedScreener: isPro,
    allowFactorHealth: isPro,
    allowBacktestExport: isPro,
    allowCustomStrategy: isPro,
    allowTeamCollaboration: isEnterprise,
    allowAPI: isEnterprise,
    allowRiskAnalytics: isPro,
    allowFactorExposure: isPro,
    allowAdvancedCharts: isPro,
  }
}
