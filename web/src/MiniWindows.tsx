import { useEffect, useRef, useState } from 'react'
import { CancellablePromise, Window } from '@wailsio/runtime'
import { ArrowRightLeft, ChartColumn, LogOut, RefreshCw, Settings2 } from 'lucide-react'
import { api } from './bridge'
import { ActionMenu, Button, Empty, Logo, MenuItem, Progress, Quota, Status } from './components'
import { useApp } from './context'
import { dateBounds, dateTime, localDate, money, quotaKeys, quotaTone, remaining } from './format'
import { useQuery } from './hooks'
import type { Provider } from './models'

// MiniWindows 共用主窗口的服务和账号数据并按内容调整原生窗口尺寸
export default function MiniWindows({ mode }: { mode: 'popover' | 'hud' }) {
  const { snapshot, tr, run, busy, revision, save } = useApp()
  const surface = useRef<HTMLDivElement>(null)
  const [clock, setClock] = useState(Date.now)
  const providers = snapshot.providers.filter((provider) => {
    const display = snapshot.settings.providers[String(provider.app)]
    return display?.enabled && (mode !== 'hud' || display.floatingHud)
  })
  const now = new Date(clock)
  const usage = useQuery(
    `surface:${revision}:${localDate(now)}`,
    () =>
      CancellablePromise.all([
        api.overview({ service: null, grain: 'day', ...dateBounds('today', '', '', now), compare: false }),
        api.overview({ service: null, grain: 'day', ...dateBounds('week', '', '', now), compare: false }),
      ]).then(([today, week]) => ({ today: today!, week: week! })),
    mode === 'popover',
  )
  useEffect(() => {
    const timer = window.setInterval(() => setClock(Date.now()), 60_000)
    return () => window.clearInterval(timer)
  }, [])
  useEffect(() => {
    const element = surface.current
    if (!element) return
    const observer = new ResizeObserver(() => {
      const height = Math.ceil(element.getBoundingClientRect().height)
      void api.resizeSurface(mode === 'hud' ? 208 : 360, height).catch(console.error)
    })
    observer.observe(element)
    return () => observer.disconnect()
  }, [mode])
  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && mode === 'popover') void api.hidePopover().catch(console.error)
      if (event.key === 'F5' || (event.ctrlKey && event.key.toLowerCase() === 'r')) void run(api.refresh)
    }
    document.addEventListener('keydown', listener)
    return () => document.removeEventListener('keydown', listener)
  }, [mode, run])
  const latest = providers
    .map((provider) => provider.lastSuccessAt)
    .filter((value): value is string => !!value)
    .sort()
    .at(-1)
  const imported = (snapshot.importedCodexAccounts ?? []).filter(
    (account) =>
      account.visibleInPopover &&
      !providers.some(
        (provider) =>
          provider.app === 0 && !!account.accountId && provider.account?.accountId === account.accountId,
      ),
  )
  const showMain = (page: string) => {
    void api.showMain(page).catch(console.error)
  }
  return mode === 'hud' ? (
    <div ref={surface} className="hud-shell">
      <ActionMenu
        label={tr('HUD actions', '悬浮窗操作')}
        trigger={
          <button className="hud-menu-handle" aria-label={tr('HUD actions', '悬浮窗操作')}>
            ···
          </button>
        }
      >
        <MenuItem onSelect={() => showMain('overview')}>{tr('Statistics', '用量统计')}</MenuItem>
        <MenuItem onSelect={() => showMain('claude-accounts')}>
          {tr('Claude accounts', 'Claude 账号')}
        </MenuItem>
        <MenuItem onSelect={() => showMain('appearance')}>{tr('Settings', '设置')}</MenuItem>
        <MenuItem
          onSelect={() => {
            void save({ ...snapshot.settings, floatingEnabled: false })
          }}
        >
          {tr('Hide HUD', '隐藏悬浮窗')}
        </MenuItem>
      </ActionMenu>
      <div
        className="hud-drag-zone"
        onPointerUp={() => {
          void Window.Position()
            .then((position) => api.hudPosition(position.x, position.y))
            .catch(console.error)
        }}
      >
        <div className="hud-rows">
          {providers.map((provider) => {
            const value = remaining(provider.snapshot?.primaryLimit?.window.usedPercent)
            return (
              <button
                className="hud-row"
                key={provider.app}
                onClick={() => showMain('overview')}
                title={provider.name}
              >
                <Logo name={quotaKeys[provider.app] ?? ''} size={19} />
                {provider.snapshot?.isUnlimited ? (
                  <span className="hud-unlimited">UNLIMITED</span>
                ) : (
                  <Progress value={value} />
                )}
                <strong className={quotaTone(value)}>
                  {provider.snapshot?.isUnlimited ? '∞' : value === undefined ? '—' : `${value.toFixed(0)}%`}
                </strong>
              </button>
            )
          })}
          {providers.length === 0 && (
            <span className="secondary">{tr('No services enabled', '未启用服务')}</span>
          )}
        </div>
      </div>
    </div>
  ) : (
    <div className="popover-shell" ref={surface}>
      <header className="popover-header">
        <div>
          <h2>{tr('Usage', '用量')}</h2>
          <p>
            {latest
              ? `${tr('Updated', '更新于')} ${dateTime(latest, snapshot.locale.startsWith('en'), true)}`
              : tr('Waiting for data', '等待数据')}
          </p>
        </div>
        <div className="actions">
          <Button
            tone="ghost"
            className="icon-button"
            disabled={busy}
            aria-label={tr('Refresh', '刷新')}
            onClick={() => run(api.refresh)}
          >
            <RefreshCw
              size={15}
              className={providers.some((provider) => provider.refreshing) ? 'spin' : ''}
            />
          </Button>
          <Button
            tone="ghost"
            className="icon-button"
            aria-label={tr('Statistics', '统计')}
            onClick={() => showMain('overview')}
          >
            <ChartColumn size={15} />
          </Button>
          <Button
            tone="ghost"
            className="icon-button"
            aria-label={tr('Settings', '设置')}
            onClick={() => showMain('services')}
          >
            <Settings2 size={15} />
          </Button>
          <Button
            tone="ghost"
            className="icon-button"
            aria-label={tr('Quit', '退出')}
            onClick={() => run(api.quit)}
          >
            <LogOut size={15} />
          </Button>
        </div>
      </header>
      {providers.map((provider) => {
        const usageApp = provider.app === 0 ? 0 : provider.app === 1 ? 1 : provider.app === 3 ? 2 : null
        return (
          <PopoverService
            key={provider.app}
            provider={provider}
            today={
              usageApp === null
                ? undefined
                : usage.data?.today.services?.find((service) => service.app === usageApp)?.totals.cost
            }
            week={
              usageApp === null
                ? undefined
                : usage.data?.week.services?.find((service) => service.app === usageApp)?.totals.cost
            }
          />
        )
      })}
      {imported.length > 0 && (
        <section className="popover-imported">
          <h3>{tr('Other Codex accounts', '其他 Codex 账号')}</h3>
          {imported.map((account) => (
            <div key={account.id} className="popover-imported-row">
              <span className="ellipsis">
                {snapshot.settings.privacyMode ? tr('Codex account', 'Codex 账号') : account.displayName}
              </span>
              <div className="mini-quotas">
                <Quota limit={account.snapshot?.primaryLimit} title="5H" compact />
                <Quota limit={account.snapshot?.secondaryLimit} title={tr('Weekly', '周额度')} compact />
              </div>
              {account.error && <p className="error-text">{account.error}</p>}
            </div>
          ))}
        </section>
      )}
      {providers.length === 0 && imported.length === 0 && (
        <Empty title={tr('No services enabled', '未启用任何服务')}>
          <Button onClick={() => showMain('services')}>{tr('Configure services', '配置服务')}</Button>
        </Empty>
      )}
    </div>
  )
}

function PopoverService({ provider, today, week }: { provider: Provider; today?: number; week?: number }) {
  const { snapshot, tr, run, busy } = useApp()
  const value = remaining(provider.snapshot?.primaryLimit?.window.usedPercent)
  const limits = [...(provider.snapshot?.auxiliaryLimits ?? []), ...(provider.snapshot?.modelLimits ?? [])]
  const identity =
    provider.account?.email ??
    provider.account?.login ??
    provider.account?.accountUuid ??
    provider.account?.accountId
  const status = snapshot.serviceStatuses[String(provider.app)]
  return (
    <section className="popover-service">
      <div className="popover-service-heading">
        <Logo name={quotaKeys[provider.app] ?? ''} size={29} />
        <h3>{provider.name}</h3>
        {snapshot.settings.showServiceStatus && status && (
          <Status kind={status === 'none' || status === 'operational' ? 'live' : 'low'} text="" />
        )}
        {provider.app === 1 && (
          <ActionMenu
            label={tr('Switch Claude account', '切换 Claude 账号')}
            trigger={
              <Button
                tone="ghost"
                className="icon-button"
                aria-label={tr('Switch Claude account', '切换 Claude 账号')}
              >
                <ArrowRightLeft size={15} />
              </Button>
            }
          >
            {(snapshot.claudeAccounts ?? []).map((account) => (
              <MenuItem
                key={account.id}
                disabled={busy || account.needsLogin || account.isActive}
                onSelect={() => {
                  void run(() => api.switchClaude(account.id))
                }}
              >
                {snapshot.settings.privacyMode ? tr('Claude account', 'Claude 账号') : account.name}
                {account.isActive ? ' ✓' : ''}
              </MenuItem>
            ))}
            <MenuItem
              onSelect={() => {
                void api.showMain('claude-accounts').catch(console.error)
              }}
            >
              {tr('Manage accounts…', '管理账号…')}
            </MenuItem>
          </ActionMenu>
        )}
      </div>
      {identity && (
        <p className="popover-identity ellipsis" title={snapshot.settings.privacyMode ? undefined : identity}>
          {snapshot.settings.privacyMode ? '••••@••••' : identity}
          {provider.snapshot?.planType ? ` · ${provider.snapshot.planType}` : ''}
        </p>
      )}
      {provider.snapshot ? (
        <>
          <div className="popover-primary">
            <div>
              <strong className={`numeric ${quotaTone(value)}`}>
                {provider.snapshot.isUnlimited ? '∞' : value === undefined ? '—' : `${value.toFixed(0)}%`}
              </strong>
              <span>{provider.snapshot.isUnlimited ? 'UNLIMITED' : tr('remaining', '剩余')}</span>
            </div>
            {!provider.snapshot.isUnlimited && (
              <Quota
                limit={provider.snapshot.primaryLimit}
                title={
                  provider.app === 3
                    ? 'TOTAL'
                    : provider.snapshot.primaryLimit?.kind === 1
                      ? tr('Weekly', '周额度')
                      : provider.snapshot.primaryLimit?.kind === 0
                        ? '5H'
                        : (provider.snapshot.primaryLimit?.displayName ?? tr('Quota', '额度'))
                }
                compact
              />
            )}
          </div>
          {provider.snapshot.secondaryLimit && (
            <Quota
              limit={provider.snapshot.secondaryLimit}
              title={
                provider.app === 3
                  ? (provider.snapshot.secondaryLimit.displayName ?? 'AUTO')
                  : tr('Weekly', '周额度')
              }
              compact
            />
          )}
          {limits.map((limit) => (
            <Quota key={limit.id} limit={limit} title={limit.displayName ?? limit.id} compact />
          ))}
          {provider.snapshot.geminiWindow && (
            <Quota
              limit={{ id: 'gemini', kind: 0, window: provider.snapshot.geminiWindow }}
              title="Gemini"
              compact
            />
          )}
          {provider.snapshot.geminiWeekly && (
            <Quota
              limit={{ id: 'gemini-weekly', kind: 1, window: provider.snapshot.geminiWeekly }}
              title={tr('Gemini weekly', 'Gemini 周额度')}
              compact
            />
          )}
        </>
      ) : (
        <p className="secondary">
          {provider.refreshing ? tr('Loading quota…', '正在读取额度…') : tr('No quota data', '暂无额度数据')}
        </p>
      )}
      {provider.error && <p className="error-text">{provider.error}</p>}
      {(provider.app <= 1 || provider.app === 3) && (
        <div className="popover-costs">
          <span>
            {tr('Today', '今天')} <strong>{today === undefined ? '—' : money(today)}</strong>
          </span>
          <span>
            {tr('This week', '本周')} <strong>{week === undefined ? '—' : money(week)}</strong>
          </span>
        </div>
      )}
    </section>
  )
}
