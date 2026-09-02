import { useToastStore } from '../store/toastStore'
import { X, AlertCircle, AlertTriangle, Info, CheckCircle } from 'lucide-react'

const typeConfig = {
  error: {
    icon: <AlertCircle className="w-5 h-5" />,
    bg: 'bg-red-50 dark:bg-red-900/30 border-red-200 dark:border-red-800',
    iconColor: 'text-red-500',
    titleColor: 'text-red-800 dark:text-red-200',
    messageColor: 'text-red-600 dark:text-red-300',
    progressColor: 'bg-red-500',
  },
  warning: {
    icon: <AlertTriangle className="w-5 h-5" />,
    bg: 'bg-yellow-50 dark:bg-yellow-900/30 border-yellow-200 dark:border-yellow-800',
    iconColor: 'text-yellow-500',
    titleColor: 'text-yellow-800 dark:text-yellow-200',
    messageColor: 'text-yellow-600 dark:text-yellow-300',
    progressColor: 'bg-yellow-500',
  },
  info: {
    icon: <Info className="w-5 h-5" />,
    bg: 'bg-blue-50 dark:bg-blue-900/30 border-blue-200 dark:border-blue-800',
    iconColor: 'text-blue-500',
    titleColor: 'text-blue-800 dark:text-blue-200',
    messageColor: 'text-blue-600 dark:text-blue-300',
    progressColor: 'bg-blue-500',
  },
  success: {
    icon: <CheckCircle className="w-5 h-5" />,
    bg: 'bg-green-50 dark:bg-green-900/30 border-green-200 dark:border-green-800',
    iconColor: 'text-green-500',
    titleColor: 'text-green-800 dark:text-green-200',
    messageColor: 'text-green-600 dark:text-green-300',
    progressColor: 'bg-green-500',
  },
}

export default function ToastContainer() {
  const { toasts, dismiss } = useToastStore()

  if (toasts.length === 0) return null

  return (
    <div className="fixed top-4 right-4 z-50 flex flex-col gap-2 max-w-sm w-full">
      {toasts.map((toast) => {
        const config = typeConfig[toast.type]
        return (
          <div
            key={toast.id}
            className={`relative flex items-start gap-3 p-3 rounded-lg border shadow-lg animate-slide-in ${config.bg}`}
            role="alert"
          >
            <span className={`shrink-0 mt-0.5 ${config.iconColor}`}>{config.icon}</span>
            <div className="flex-1 min-w-0">
              <p className={`text-sm font-semibold ${config.titleColor}`}>{toast.title}</p>
              {toast.message && (
                <p className={`text-xs mt-0.5 ${config.messageColor} break-words`}>{toast.message}</p>
              )}
            </div>
            <button
              onClick={() => dismiss(toast.id)}
              className={`shrink-0 ${config.messageColor} hover:opacity-70 p-0.5 -m-0.5`}
            >
              <X className="w-3.5 h-3.5" />
            </button>
            <div
              className={`absolute bottom-0 left-0 h-0.5 ${config.progressColor} rounded-b-lg toast-progress`}
              style={{
                width: '100%',
                animationDuration: `${toast.duration}ms`,
              }}
            />
          </div>
        )
      })}
    </div>
  )
}
