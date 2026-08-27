import { useState } from 'react'
import { useAppStore } from '../store/appStore'
import { useI18nStore } from '../store/i18nStore'
import { Rocket, ChevronRight, Check, Globe, Cpu, Shield, AlertTriangle } from 'lucide-react'
import TitleBar from '../components/TitleBar'

// Wails 后端 API 访问助手（与 Settings.tsx 保持一致）
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

// 各 AI 提供商的默认模型与接口地址
const AI_PROVIDER_DEFAULTS: Record<string, { model: string; baseURL: string }> = {
  deepseek: { model: 'deepseek-chat', baseURL: 'https://api.deepseek.com' },
  openai: { model: 'gpt-4o', baseURL: 'https://api.openai.com/v1' },
  claude: { model: 'claude-3-5-sonnet', baseURL: 'https://api.anthropic.com' },
  other: { model: '', baseURL: '' },
}

export default function WelcomeWizard() {
  const setFirstRun = useAppStore((state) => state.setFirstRun)
  const t = useI18nStore((s) => s.t)
  const { language, setLanguage } = useI18nStore()

  const [step, setStep] = useState(1)
  const [capital, setCapital] = useState('100000')
  const [aiProvider, setAiProvider] = useState('deepseek')
  const [apiKey, setApiKey] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const handleFinish = async () => {
    const capitalNum = Number(capital)
    if (!Number.isFinite(capitalNum) || capitalNum <= 0) {
      setError('初始资金必须为正数')
      return
    }
    setSaving(true)
    setError('')
    try {
      await callApp<void>('SetInitialCapital', capitalNum)
      if (apiKey.trim()) {
        const def = AI_PROVIDER_DEFAULTS[aiProvider] || AI_PROVIDER_DEFAULTS.other
        await callApp<void>('SetAISettings', aiProvider, def.model, def.baseURL, apiKey.trim())
      }
      setFirstRun(false)
    } catch (err) {
      setError(`配置保存失败: ${err instanceof Error ? err.message : String(err)}`)
    } finally {
      setSaving(false)
    }
  }

  const nextStep = () => setStep((s) => Math.min(s + 1, 4))
  const prevStep = () => setStep((s) => Math.max(s - 1, 1))

  const stepText = t('welcome.stepOf').replace('{current}', String(step)).replace('{total}', '4')

  return (
    <div className="flex flex-col h-full w-full bg-slate-50 dark:bg-slate-900 transition-colors overflow-hidden">
      <TitleBar title="QuantBot" subtitle="Setup Wizard" />
      <div className="flex-1 flex items-center justify-center p-6 overflow-auto">
      <div className="max-w-lg w-full">
        {/* Step indicator */}
        <div className="flex items-center justify-center gap-2 mb-6">
          {[1, 2, 3, 4].map((s) => (
            <div
              key={s}
              className={`w-8 h-2 rounded-full transition-colors ${
                s <= step ? 'bg-brand-500' : 'bg-slate-200 dark:bg-slate-700'
              }`}
            />
          ))}
        </div>

        <div className="bg-white dark:bg-slate-800 rounded-2xl shadow-lg p-8 transition-colors">
          <div className="text-center mb-8">
            <div className="w-14 h-14 mx-auto mb-4 bg-brand-50 dark:bg-brand-900/30 rounded-2xl flex items-center justify-center">
              <Rocket className="w-7 h-7 text-brand-500" />
            </div>
            <h1 className="text-2xl font-bold text-slate-800 dark:text-slate-100">{t('welcome.title')}</h1>
            <p className="text-slate-500 dark:text-slate-400 mt-2 text-sm">
              {step === 1 && t('welcome.step1Desc')}
              {step === 2 && t('welcome.step2Desc')}
              {step === 3 && t('welcome.aiSetup')}
              {step === 4 && t('welcome.riskos')}
            </p>
          </div>

          {/* Step 1: Market & Capital */}
          {step === 1 && (
            <div className="space-y-5">
              <div className="flex items-center justify-center gap-2 mb-2">
                <Globe className="w-4 h-4 text-slate-400" />
                <span className="text-xs text-slate-500 dark:text-slate-400">{stepText}</span>
              </div>

              <div>
                <label className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">
                  {t('welcome.market')}
                </label>
                <div className="grid grid-cols-1 gap-2">
                  <button
                    disabled
                    className="px-3 py-2.5 rounded-lg text-sm font-medium bg-brand-500 text-white opacity-80 cursor-not-allowed"
                  >
                    {t('settings.cn')} {t('settings.aStockOnly')}
                  </button>
                </div>
              </div>

              <div>
                <label className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">
                  {t('welcome.initialCapital')}
                </label>
                <input
                  type="number"
                  value={capital}
                  onChange={(e) => setCapital(e.target.value)}
                  className="input-field"
                />
              </div>
            </div>
          )}

          {/* Step 2: AI Provider */}
          {step === 2 && (
            <div className="space-y-5">
              <div className="flex items-center justify-center gap-2 mb-2">
                <Cpu className="w-4 h-4 text-slate-400" />
                <span className="text-xs text-slate-500 dark:text-slate-400">{stepText}</span>
              </div>

              <div>
                <label className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">
                  {t('welcome.selectAI')}
                </label>
                <div className="grid grid-cols-2 gap-2">
                  {[
                    { value: 'deepseek', label: 'DeepSeek' },
                    { value: 'openai', label: 'OpenAI' },
                    { value: 'claude', label: 'Claude' },
                    { value: 'other', label: 'Other' },
                  ].map((p) => (
                    <button
                      key={p.value}
                      onClick={() => setAiProvider(p.value)}
                      className={`px-3 py-2.5 rounded-lg text-sm font-medium transition-colors ${
                        aiProvider === p.value
                          ? 'bg-brand-500 text-white'
                          : 'bg-slate-100 text-slate-700 hover:bg-slate-200 dark:bg-slate-700 dark:text-slate-300 dark:hover:bg-slate-600'
                      }`}
                    >
                      {p.label}
                    </button>
                  ))}
                </div>
              </div>

              <div>
                <label className="block text-sm font-medium text-slate-700 dark:text-slate-300 mb-2">
                  {t('welcome.enterKey')}
                </label>
                <input
                  type="password"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder="sk-..."
                  className="input-field"
                />
              </div>
            </div>
          )}

          {/* Step 3: RiskOS */}
          {step === 3 && (
            <div className="space-y-5">
              <div className="flex items-center justify-center gap-2 mb-2">
                <Shield className="w-4 h-4 text-slate-400" />
                <span className="text-xs text-slate-500 dark:text-slate-400">{stepText}</span>
              </div>

              <div className="bg-slate-50 dark:bg-slate-700/50 rounded-xl p-6 text-center">
                <div className="w-12 h-12 mx-auto mb-3 bg-brand-50 dark:bg-brand-900/30 rounded-full flex items-center justify-center">
                  <Shield className="w-6 h-6 text-brand-500" />
                </div>
                <h3 className="font-semibold text-slate-800 dark:text-slate-100 mb-2">{t('welcome.riskos')}</h3>
                <p className="text-sm text-slate-500 dark:text-slate-400 mb-4">
                  {t('welcome.riskosDesc')}
                </p>
                <button className="btn-primary w-full">{t('welcome.riskos')}</button>
              </div>
            </div>
          )}

          {/* Step 4: Complete */}
          {step === 4 && (
            <div className="space-y-5">
              <div className="flex items-center justify-center gap-2 mb-2">
                <Check className="w-4 h-4 text-green-500" />
                <span className="text-xs text-slate-500 dark:text-slate-400">{stepText}</span>
              </div>

              <div className="bg-green-50 dark:bg-green-900/20 border border-green-200 dark:border-green-800 rounded-xl p-6 text-center">
                <div className="w-12 h-12 mx-auto mb-3 bg-green-100 dark:bg-green-900/40 rounded-full flex items-center justify-center">
                  <Check className="w-6 h-6 text-green-500" />
                </div>
                <h3 className="font-semibold text-green-800 dark:text-green-300 mb-2">{t('welcome.setupComplete')}</h3>
                <p className="text-sm text-green-600 dark:text-green-400">
                  {t('welcome.initialCapital')}: ${capital.toLocaleString()}
                </p>
              </div>

              <div className="flex items-center justify-between text-sm">
                <span className="text-slate-500 dark:text-slate-400">{t('settings.language')}</span>
                <div className="flex gap-2">
                  {(['zh', 'en'] as const).map((lang) => (
                    <button
                      key={lang}
                      onClick={() => setLanguage(lang)}
                      className={`px-3 py-1 rounded text-xs font-medium transition-colors ${
                        language === lang
                          ? 'bg-brand-500 text-white'
                          : 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-400'
                      }`}
                    >
                      {lang === 'zh' ? '中文' : 'EN'}
                    </button>
                  ))}
                </div>
              </div>
            </div>
          )}

          {/* Navigation */}
          {error && (
            <div className="mt-4 flex items-center gap-2 px-3 py-2 rounded-lg text-sm text-red-600 dark:text-red-400 bg-red-50 dark:bg-red-900/20 border border-red-200 dark:border-red-800">
              <AlertTriangle className="w-4 h-4 shrink-0" />
              {error}
            </div>
          )}
          <div className="flex items-center justify-between mt-8 pt-6 border-t border-slate-200 dark:border-slate-700">
            {step > 1 ? (
              <button onClick={prevStep} className="btn-secondary" disabled={saving}>
                {t('welcome.back')}
              </button>
            ) : (
              <div />
            )}

            {step < 4 ? (
              <button onClick={nextStep} className="btn-primary flex items-center gap-1">
                {t('welcome.next')}
                <ChevronRight className="w-4 h-4" />
              </button>
            ) : (
              <button onClick={handleFinish} disabled={saving} className="btn-primary flex items-center gap-1">
                {saving ? '保存中...' : t('welcome.launch')}
                <ChevronRight className="w-4 h-4" />
              </button>
            )}
          </div>
        </div>
      </div>
      </div>
    </div>
  )
}
