import { startTransition, useEffect, useEffectEvent, useRef, useState, type ReactNode } from 'react'
import { flushSync } from 'react-dom'
import { Events } from '@wailsio/runtime'
import { ChartColumn, Gauge, History, List, LoaderCircle, UserRound } from 'lucide-react'
import { toast, Toaster } from 'sonner'
import { api } from './bridge'
import { AppContext } from './context'
import { Button, Logo, Segments } from './components'
import { usageKeys, usageNames } from './format'
import type { Settings as SettingsModel, Snapshot } from './models'
import Accounts from './Accounts'
import MiniWindows from './MiniWindows'
import Onboarding from './Onboarding'
import Settings from './Settings'
import Statistics from './Statistics'
import ThemeToggle from './ThemeToggle'
import WindowControls from './WindowControls'

const settingPages = ['services', 'claude-accounts', 'appearance', 'data', 'general']

// App 统一订阅 Go 状态并将原生菜单导航交给页面路由
export default function App() {
  const [snapshot, setSnapshot] = useState<Snapshot>()
  const [revision, setRevision] = useState(0)
  const [page, setCurrentPage] = useState(new URLSearchParams(location.search).get('page') ?? 'overview')
  const [statisticsPage, setStatisticsPage] = useState(() =>
    settingPages.includes(page) ? 'overview' : page,
  )
  const [service, setService] = useState<number | null>(null)
  const [operations, setOperations] = useState(0)
  const [initialError, setInitialError] = useState('')
  const dataSignature = useRef('')
  const mode = new URLSearchParams(location.search).get('window') ?? 'main'
  const english =
    (snapshot?.settings.language === 'system' ? snapshot.locale : snapshot?.settings.language)?.startsWith(
      'en',
    ) ?? false
  const tr = (en: string, zh: string) => (english ? en : zh)
  const setPage = (next: string) => {
    setCurrentPage(next)
    if (!settingPages.includes(next)) setStatisticsPage(next)
  }
  function applySnapshot(value: Snapshot) {
    const signature = JSON.stringify([
      value.scan.revision,
      value.settings.usageVisibility,
      value.settings.providers['3']?.enabled,
    ])
    if (signature !== dataSignature.current) {
      dataSignature.current = signature
      setRevision((previous) => previous + 1)
    }
    setSnapshot(value)
  }
  async function refresh() {
    applySnapshot(await api.snapshot())
  }
  const fetchSnapshot = useEffectEvent(() => api.snapshot())
  useEffect(() => {
    let mounted = true
    const reload = () => {
      void fetchSnapshot()
        .then((value) => {
          if (mounted) {
            startTransition(() => applySnapshot(value))
            setInitialError('')
          }
        })
        .catch((cause: unknown) => {
          if (mounted) setInitialError(cause instanceof Error ? cause.message : String(cause))
        })
    }
    reload()
    const changed = Events.On('ccbar:changed', reload)
    const unsubscribeNavigate = Events.On('ccbar:navigate', (event) => {
      if (mode !== 'main') return
      const data: unknown = event.data
      if (typeof data === 'object' && data && 'page' in data && typeof data.page === 'string') {
        const next =
          data.page === 'statistics' ? 'overview' : data.page === 'settings' ? 'services' : data.page
        flushSync(() => setPage(next))
        void api.presentMain().catch(console.error)
      }
    })
    return () => {
      mounted = false
      changed()
      unsubscribeNavigate()
    }
  }, [mode])
  useEffect(() => {
    document.documentElement.lang = english ? 'en' : 'zh-CN'
  }, [english])
  useEffect(() => {
    document.documentElement.dataset.theme = snapshot?.settings.theme ?? 'system'
  }, [snapshot?.settings.theme])
  async function run(operation: () => Promise<unknown>, success?: string): Promise<boolean> {
    setOperations((value) => value + 1)
    try {
      await operation()
      await refresh()
      if (success) toast.success(success, { duration: 3000 })
      return true
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : String(cause), { duration: 6000 })
      return false
    } finally {
      setOperations((value) => value - 1)
    }
  }
  async function save(settings: SettingsModel): Promise<boolean> {
    try {
      await api.saveSettings(settings)
      await refresh()
      return true
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : String(cause), { duration: 6000 })
      return false
    }
  }
  if (!snapshot)
    return (
      <div className="startup">
        <img src="./ccbar-icon.png" alt="CCBar" />
        <h1>CCBar</h1>
        {initialError ? (
          <>
            <p role="alert">{initialError}</p>
            <Button
              onClick={() => {
                void refresh().catch((cause: unknown) =>
                  setInitialError(cause instanceof Error ? cause.message : String(cause)),
                )
              }}
            >
              {tr('Retry', '重试')}
            </Button>
          </>
        ) : (
          <LoaderCircle className="spin" size={22} />
        )}
      </div>
    )
  const settingsActive = settingPages.includes(page)
  const available = usageNames
    .map((name, app) => ({ name, app }))
    .filter(
      ({ app }) =>
        snapshot.settings.usageVisibility[String(app)] &&
        (app !== 2 || snapshot.settings.providers['3']?.enabled),
    )
  const selectedService = available.some((item) => item.app === service) ? service : null
  const context = {
    snapshot,
    english,
    tr,
    refresh,
    revision,
    busy: operations > 0,
    run,
    save,
    navigate: setPage,
  }
  return (
    <AppContext.Provider value={context}>
      {mode === 'popover' || mode === 'hud' ? (
        <MiniWindows mode={mode} />
      ) : (
        <div className="app-shell">
          <header className="app-header">
            <div className="app-brand">
              <img src="./ccbar-icon.png" alt="" />
              <strong>CCBar</strong>
            </div>
            <Segments
              value={settingsActive ? 'settings' : 'statistics'}
              onChange={(value) => setPage(value === 'settings' ? 'services' : statisticsPage)}
              label={tr('Workspace', '工作区')}
              options={[
                { value: 'statistics', label: tr('Statistics', '用量统计') },
                { value: 'settings', label: tr('Settings', '设置') },
              ]}
            />
            <div className="app-header-status">
              {snapshot.scan.isScanning && <LoaderCircle size={14} className="spin" />}
              <WindowControls />
            </div>
          </header>
          <aside className="sidebar">
            {settingsActive ? (
              <>
                <div className="sidebar-caption">{tr('Preferences', '偏好设置')}</div>
                <Nav
                  page="services"
                  current={page}
                  onSelect={setPage}
                  label={tr('Services & Accounts', '服务与账号')}
                />
                <Nav
                  page="claude-accounts"
                  current={page}
                  onSelect={setPage}
                  icon={<UserRound size={17} />}
                  label={tr('Claude Accounts', 'Claude 账号')}
                />
                <Nav
                  page="appearance"
                  current={page}
                  onSelect={setPage}
                  label={tr('Appearance & Display', '外观与显示')}
                />
                <Nav
                  page="data"
                  current={page}
                  onSelect={setPage}
                  label={tr('Data & Refresh', '数据与刷新')}
                />
                <Nav page="general" current={page} onSelect={setPage} label={tr('General', '通用')} />
              </>
            ) : (
              <>
                <div className="sidebar-caption">{tr('Service', '服务')}</div>
                <button
                  className={`nav-item ${selectedService === null ? 'selected' : ''}`}
                  onClick={() => setService(null)}
                >
                  <span>{tr('All', '全部')}</span>
                </button>
                {available.map(({ name, app }) => (
                  <button
                    className={`nav-item ${selectedService === app ? 'selected' : ''}`}
                    key={app}
                    onClick={() => setService(app)}
                  >
                    <Logo name={usageKeys[app] ?? ''} size={16} />
                    <span>{name}</span>
                  </button>
                ))}
                <div className="sidebar-caption services-label">{tr('View', '视图')}</div>
                <Nav
                  page="overview"
                  current={page}
                  onSelect={setPage}
                  icon={<ChartColumn size={17} />}
                  label={tr('Overview', '概览')}
                />
                <Nav
                  page="conversations"
                  current={page}
                  onSelect={setPage}
                  icon={<List size={17} />}
                  label={tr('Conversations', '对话')}
                />
                <Nav
                  page="timeline"
                  current={page}
                  onSelect={setPage}
                  icon={<History size={17} />}
                  label={tr('Timeline', '时间线')}
                />
                <Nav
                  page="cycles"
                  current={page}
                  onSelect={setPage}
                  icon={<Gauge size={17} />}
                  label={tr('Cycles', '周期')}
                />
              </>
            )}
            <ThemeToggle />
          </aside>
          <main className="main-content">
            <div hidden={settingsActive}>
              <Statistics page={statisticsPage} service={selectedService} />
            </div>
            {page === 'claude-accounts' ? <Accounts /> : settingsActive ? <Settings page={page} /> : null}
          </main>
          <Onboarding />
        </div>
      )}
      <Toaster
        theme={snapshot.settings.theme}
        position="bottom-right"
        richColors
        closeButton
        toastOptions={{ closeButtonAriaLabel: tr('Dismiss', '关闭') }}
      />
    </AppContext.Provider>
  )
}

function Nav({
  page,
  current,
  onSelect,
  label,
  icon,
}: {
  page: string
  current: string
  onSelect: (page: string) => void
  label: string
  icon?: ReactNode
}) {
  return (
    <button
      className={`nav-item ${page === current ? 'selected' : ''}`}
      aria-current={page === current ? 'page' : undefined}
      onClick={() => onSelect(page)}
    >
      {icon}
      <span>{label}</span>
    </button>
  )
}
