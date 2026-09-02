import { create } from 'zustand'

export type Theme = 'light' | 'dark'

interface ThemeState {
  theme: Theme
  setTheme: (theme: Theme) => void
  toggleTheme: () => void
}

const getInitialTheme = (): Theme => {
  try {
    const saved = localStorage.getItem('theme') as Theme | null
    if (saved === 'light' || saved === 'dark') return saved
    if (typeof window !== 'undefined' && window.matchMedia('(prefers-color-scheme: dark)').matches) return 'dark'
  } catch {
    // localStorage or matchMedia not available
  }
  return 'light'
}

const applyTheme = (theme: Theme) => {
  if (typeof document !== 'undefined') {
    const root = document.documentElement
    if (theme === 'dark') {
      root.classList.add('dark')
    } else {
      root.classList.remove('dark')
    }
  }
}

const initialTheme = getInitialTheme()
applyTheme(initialTheme)

export const useThemeStore = create<ThemeState>((set) => ({
  theme: initialTheme,
  setTheme: (theme) => {
    applyTheme(theme)
    try {
      localStorage.setItem('theme', theme)
    } catch {
      // localStorage not available
    }
    set({ theme })
  },
  toggleTheme: () => {
    set((state) => {
      const newTheme = state.theme === 'light' ? 'dark' : 'light'
      applyTheme(newTheme)
      try {
        localStorage.setItem('theme', newTheme)
      } catch {
        // localStorage not available
      }
      return { theme: newTheme }
    })
  },
}))
