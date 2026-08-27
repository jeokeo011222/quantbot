import { create } from 'zustand'
import { translations } from '../i18n/translations'
import type { TranslationKeys } from '../i18n/translations'

export type Language = 'zh' | 'en'

interface I18nState {
  language: Language
  setLanguage: (lang: Language) => void
  t: (key: string) => string
}

const createTFunction = (lang: Language) => {
  return (key: string): string => {
    return translations[lang][key] || translations.en[key] || key
  }
}

const getInitialLanguage = (): Language => {
  try {
    const saved = localStorage.getItem('language') as Language | null
    if (saved === 'zh' || saved === 'en') return saved
    const browserLang = typeof navigator !== 'undefined' ? navigator.language.toLowerCase() : ''
    if (browserLang.startsWith('zh')) return 'zh'
  } catch {
    // localStorage or navigator not available
  }
  return 'en'
}

const initialLanguage = getInitialLanguage()

export const useI18nStore = create<I18nState>((set) => ({
  language: initialLanguage,
  setLanguage: (lang: Language) => {
    try {
      localStorage.setItem('language', lang)
    } catch {
      // localStorage not available
    }
    set({ language: lang, t: createTFunction(lang) })
  },
  t: createTFunction(initialLanguage),
}))

export type { TranslationKeys }
