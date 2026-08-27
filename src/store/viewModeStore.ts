import { create } from 'zustand'

export type ViewMode = 'simple' | 'detailed'

interface ViewModeState {
  mode: ViewMode
  setMode: (mode: ViewMode) => void
  toggleMode: () => void
}

const STORAGE_KEY = 'quantpilot_view_mode'

function getInitialMode(): ViewMode {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored === 'simple' || stored === 'detailed') return stored
  } catch {
    // ignore
  }
  return 'simple'
}

export const useViewModeStore = create<ViewModeState>((set, get) => ({
  mode: getInitialMode(),
  setMode: (mode: ViewMode) => {
    try {
      localStorage.setItem(STORAGE_KEY, mode)
    } catch {
      // ignore
    }
    set({ mode })
  },
  toggleMode: () => {
    const current = get().mode
    const next: ViewMode = current === 'simple' ? 'detailed' : 'simple'
    try {
      localStorage.setItem(STORAGE_KEY, next)
    } catch {
      // ignore
    }
    set({ mode: next })
  },
}))
