import { create } from 'zustand'

export interface Toast {
  id: string
  type: 'error' | 'warning' | 'info' | 'success'
  title: string
  message?: string
  duration: number
}

interface ToastState {
  toasts: Toast[]
  showToast: (toast: Omit<Toast, 'id' | 'duration'> & { duration?: number }) => void
  error: (title: string, message?: string) => void
  warning: (title: string, message?: string) => void
  info: (title: string, message?: string) => void
  success: (title: string, message?: string) => void
  dismiss: (id: string) => void
}

export const useToastStore = create<ToastState>((set) => ({
  toasts: [],

  showToast: (toast) => {
    const id = `toast-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    const duration = toast.duration ?? 5000
    const newToast: Toast = { ...toast, id, duration }
    set((state) => ({ toasts: [...state.toasts, newToast] }))
    setTimeout(() => {
      set((state) => ({ toasts: state.toasts.filter((t) => t.id !== id) }))
    }, duration)
  },

  error: (title, message) => {
    const id = `toast-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    const duration = 7000
    set((state) => ({
      toasts: [...state.toasts, { id, type: 'error', title, message, duration }],
    }))
    setTimeout(() => {
      set((state) => ({ toasts: state.toasts.filter((t) => t.id !== id) }))
    }, duration)
  },

  warning: (title, message) => {
    const id = `toast-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    const duration = 6000
    set((state) => ({
      toasts: [...state.toasts, { id, type: 'warning', title, message, duration }],
    }))
    setTimeout(() => {
      set((state) => ({ toasts: state.toasts.filter((t) => t.id !== id) }))
    }, duration)
  },

  info: (title, message) => {
    const id = `toast-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    const duration = 5000
    set((state) => ({
      toasts: [...state.toasts, { id, type: 'info', title, message, duration }],
    }))
    setTimeout(() => {
      set((state) => ({ toasts: state.toasts.filter((t) => t.id !== id) }))
    }, duration)
  },

  success: (title, message) => {
    const id = `toast-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
    const duration = 4000
    set((state) => ({
      toasts: [...state.toasts, { id, type: 'success', title, message, duration }],
    }))
    setTimeout(() => {
      set((state) => ({ toasts: state.toasts.filter((t) => t.id !== id) }))
    }, duration)
  },

  dismiss: (id) => {
    set((state) => ({ toasts: state.toasts.filter((t) => t.id !== id) }))
  },
}))
