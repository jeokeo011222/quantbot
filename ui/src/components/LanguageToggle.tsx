import { Languages } from 'lucide-react'
import { useI18nStore } from '../store/i18nStore'
import type { Language } from '../store/i18nStore'

export default function LanguageToggle() {
  const { language, setLanguage } = useI18nStore()

  const toggle = () => {
    const newLang: Language = language === 'zh' ? 'en' : 'zh'
    setLanguage(newLang)
  }

  return (
    <button
      onClick={toggle}
      className="flex items-center gap-1 px-2 py-1.5 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-700 transition-colors text-xs font-medium text-slate-600 dark:text-slate-400"
      title={language === 'zh' ? 'Switch to English' : '切换到中文'}
      aria-label="Toggle language"
    >
      <Languages className="w-3.5 h-3.5" />
      <span>{language === 'zh' ? 'EN' : '中'}</span>
    </button>
  )
}
