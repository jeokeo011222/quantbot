import { Minus, Square, X, LineChart, AlertTriangle } from 'lucide-react'
import { WindowMinimise, WindowToggleMaximise, WindowQuit } from '../../wailsjs/go/main/App'

interface TitleBarProps {
  title?: string
  subtitle?: string
  disclaimer?: string
}

export default function TitleBar({ title = 'QuantBot', subtitle = 'AI量化机器人', disclaimer = '' }: TitleBarProps) {
  const handleMinimize = () => {
    WindowMinimise()
  }

  const handleMaximize = () => {
    WindowToggleMaximise()
  }

  const handleClose = () => {
    WindowQuit()
  }

  return (
    <div
      className="relative h-10 min-h-[40px] shrink-0 grow-0 bg-slate-100 dark:bg-slate-800 border-b border-slate-200 dark:border-slate-700 flex items-center justify-between select-none"
      data-wails-drag
    >
      {/* Left: Logo & Title */}
      <div className="flex items-center gap-3 px-4 flex-1 min-w-0">
        <div className="w-5 h-5 rounded bg-gradient-to-br from-blue-500 to-cyan-400 flex items-center justify-center shadow-sm shrink-0">
          <LineChart className="w-3 h-3 text-white" />
        </div>
        <span className="text-sm font-semibold text-slate-700 dark:text-slate-200 truncate">{title}</span>
        <span className="text-xs text-slate-400 dark:text-slate-500 hidden lg:inline truncate">{subtitle}</span>
      </div>

      {/* Center: Disclaimer (red warning) */}
      {disclaimer && (
        <div className="absolute left-1/2 -translate-x-1/2 hidden md:flex items-center gap-1.5 px-3 max-w-[45%] pointer-events-none">
          <AlertTriangle className="w-3.5 h-3.5 text-red-500 dark:text-red-400 shrink-0" />
          <span className="text-[11px] font-medium text-red-600 dark:text-red-400 truncate">{disclaimer}</span>
        </div>
      )}

      {/* Right: Window Controls */}
      <div className="flex items-center h-full">
        {/* Minimize */}
        <button
          onClick={handleMinimize}
          className="w-11 h-full flex items-center justify-center text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-100 hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors"
          title="最小化"
        >
          <Minus className="w-3.5 h-3.5" />
        </button>

        {/* Maximize/Restore */}
        <button
          onClick={handleMaximize}
          className="w-11 h-full flex items-center justify-center text-slate-500 hover:text-slate-800 dark:text-slate-400 dark:hover:text-slate-100 hover:bg-slate-200 dark:hover:bg-slate-700 transition-colors"
          title="最大化"
        >
          <Square className="w-3 h-3" />
        </button>

        {/* Close */}
        <button
          onClick={handleClose}
          className="w-11 h-full flex items-center justify-center text-slate-500 hover:text-white dark:text-slate-400 hover:bg-red-500 transition-colors group"
          title="关闭"
        >
          <X className="w-3.5 h-3.5 group-hover:text-white" />
        </button>
      </div>
    </div>
  )
}
