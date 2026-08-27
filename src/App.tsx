import { useState, useEffect } from 'react'
import Layout from './components/Layout'
import InitialLoading from './components/InitialLoading'
import ToastContainer from './components/ToastContainer'
import TradeApprovalModal from './components/TradeApprovalModal'
import Dashboard from './pages/Dashboard'
import CIO from './pages/CIO'
import AITeam from './pages/AITeam'
import Activity from './pages/Activity'
import Strategies from './pages/Strategies'
import Backtest from './pages/Backtest'
import Portfolio from './pages/Portfolio'
import InvestmentPlanner from './pages/InvestmentPlanner'
import StockScreener from './pages/StockScreener'
import ETFMonitor from './pages/ETFMonitor'
import Settings from './pages/Settings'
import WelcomeWizard from './pages/WelcomeWizard'
import { useAppStore } from './store/appStore'

function AppRouter() {
  const isFirstRun = useAppStore((state) => state.isFirstRun)
  const [isAppReady, setIsAppReady] = useState(false)
  const [hash, setHash] = useState(getHash())

  useEffect(() => {
    const handler = () => setHash(getHash())
    window.addEventListener('hashchange', handler)
    return () => window.removeEventListener('hashchange', handler)
  }, [])

  // 显示初始加载界面，等待系统初始化完成
  if (!isAppReady) {
    return <InitialLoading onReady={() => setIsAppReady(true)} />
  }

  if (isFirstRun) {
    return <WelcomeWizard />
  }

  let page
  switch (hash) {
    case '/cio':
      page = <CIO />
      break
    case '/ai-team':
      page = <AITeam />
      break
    case '/workflow':
      page = <Activity />
      break
    case '/activity':
      page = <Activity />
      break
    case '/screener':
      page = <StockScreener />
      break
    case '/etf-monitor':
      page = <ETFMonitor />
      break
    case '/strategies':
      page = <Strategies />
      break
    case '/backtest':
      page = <Backtest />
      break
    case '/portfolio':
      page = <Portfolio />
      break
    case '/investment-planner':
      page = <InvestmentPlanner />
      break
    case '/settings':
      page = <Settings />
      break
    default:
      page = <Dashboard />
  }

  return (
    <>
      <Layout>{page}</Layout>
      <ToastContainer />
      <TradeApprovalModal />
    </>
  )
}

function getHash(): string {
  const h = window.location.hash.replace(/^#/, '')
  return h || '/'
}

export default function App() {
  return <AppRouter />
}
