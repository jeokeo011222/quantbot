import { Sun, Moon } from 'lucide-react'
import { useThemeStore } from '../store/themeStore'
import { useI18nStore } from '../store/i18nStore'

export default function ThemeToggle() {
  const { theme, toggleTheme } = useThemeStore()
  const t = useI18nStore((s) => s.t)

  return (
    <button
      onClick={toggleTheme}
      className="p-2 rounded-lg hover:bg-slate-100 dark:hover:bg-slate-700 transition-colors"
      title={theme === 'light' ? t('settings.dark') : t('settings.light')}
      aria-label="Toggle theme"
    >
      {theme === 'light' ? (
        <Moon className="w-4 h-4 text-slate-600" />
      ) : (
        <Sun className="w-4 h-4 text-yellow-400" />
      )}
    </button>
  )
}
