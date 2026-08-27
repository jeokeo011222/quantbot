import { useState } from 'react'
import logo1 from '../../img/logo1.png'

interface AppLogoProps {
  size?: number
  className?: string
  rounded?: string
}

// 统一应用 Logo：使用 ui/img/logo1.png 作为本系统 logo/图标
export default function AppLogo({ size = 36, className = '', rounded = 'rounded-xl' }: AppLogoProps) {
  const [failed, setFailed] = useState(false)

  if (!failed) {
    return (
      <img
        src={logo1}
        alt="QuantBot"
        width={size}
        height={size}
        onError={() => setFailed(true)}
        className={`shrink-0 object-cover ${rounded} ${className}`}
        style={{ width: size, height: size }}
        draggable={false}
      />
    )
  }

  // 兜底：logo1.png 加载失败时渲染同款内联 SVG
  return (
    <div
      className={`bg-gradient-to-br from-blue-500 to-cyan-400 flex items-center justify-center shadow-sm shrink-0 ${rounded} ${className}`}
      style={{ width: size, height: size }}
    >
      <svg
        viewBox="0 0 64 64"
        width={size * 0.6}
        height={size * 0.6}
        fill="none"
        xmlns="http://www.w3.org/2000/svg"
      >
        <polyline
          points="12,44 24,36 32,40 42,24 52,20"
          stroke="#FFFFFF"
          strokeWidth="3"
          fill="none"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
        <circle cx="24" cy="36" r="3" fill="#FFFFFF" />
        <circle cx="32" cy="40" r="3" fill="#FFFFFF" />
        <circle cx="42" cy="24" r="3" fill="#FFFFFF" />
        <circle cx="52" cy="20" r="4" fill="#FFFFFF" />
        <polyline points="48,14 52,20 56,14" stroke="#FFFFFF" strokeWidth="2.5" fill="none" strokeLinecap="round" strokeLinejoin="round" />
        <rect x="18" y="46" width="2" height="8" fill="#FFFFFF" opacity="0.6" />
        <rect x="22" y="48" width="2" height="6" fill="#FFFFFF" opacity="0.6" />
        <rect x="38" y="44" width="2" height="10" fill="#FFFFFF" opacity="0.6" />
        <rect x="46" y="42" width="2" height="12" fill="#FFFFFF" opacity="0.6" />
      </svg>
    </div>
  )
}
