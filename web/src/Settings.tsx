import { useState } from 'react'
import {
  ArrowDown,
  ArrowUp,
  ArrowRightLeft,
  KeyRound,
  Plus,
  RefreshCw,
  Download,
  LoaderCircle,
  Sparkles,
  FolderOpen,
} from 'lucide-react'
import { api } from './bridge'
import { useApp } from './context'
import {
  ActionMenu,
  Button,
  Card,
  Dialog,
  Empty,
  Logo,
  MenuItem,
  Quota,
  QueryState,
  Section,
  Segments,
  Select,
  SettingRow,
  Status,
  Switch,
} from './components'
import { dateTime, quotaKeys, usageKeys, usageNames } from './format'
import type { CodexAccount, Provider, ReleaseUpdate, Settings as SettingsModel } from './models'
import ResetCredits from './ResetCredits'
import { useQuery } from './hooks'

export default function Settings({ page }: { page: string }) {
  const { tr } = useApp()
  const title =
    page === 'services'
      ? tr('Services & Accounts', '服务与账号')
      : page === 'appearance'
        ? tr('Appearance & Display', '外观与显示')
        : page === 'data'
          ? tr('Data & Refresh', '数据与刷新')
          : tr('General', '通用')
  return (
    <div className="settings-content">
      <header className="page-heading">
        <h1>{title}</h1>
      </header>
      {page === 'services' ? (
        <Services />
      ) : page === 'appearance' ? (
        <Appearance />
      ) : page === 'data' ? (
        <Data />
      ) : (
        <General />
      )}
    </div>
  )
}

function Services() {
  const { snapshot, tr, busy, save, navigate, run } = useApp()
  const [codexImport, setCodexImport] = useState(false)
  const [credits, setCredits] = useState<{ id: string; name: string }>()
  const [commandKey, setCommandKey] = useState(false)
  const [remove, setRemove] = useState<CodexAccount>()
  const [dragging, setDragging] = useState<string>()
  const settings = snapshot.settings
  const accounts = snapshot.importedCodexAccounts ?? []
  const update = (key: keyof SettingsModel, value: SettingsModel[typeof key]) => {
    void save({ ...settings, [key]: value })
  }
  const reorder = (from: string, to: string) => {
    const list = [...accounts]
    const index = list.findIndex((account) => account.id === from)
    const targetIndex = list.findIndex((account) => account.id === to)
    const selected = list[index]
    if (!selected || from === to) return
    list.splice(index, 1)
    list.splice(targetIndex, 0, selected)
    void run(() => api.saveCodex(list))
  }
  return (
    <>
      <Section
        title={tr('Connected services', '已接入服务')}
        description={tr(
          'Quota monitoring, tray, floating HUD and local usage.',
          '配额监控、通知区域、悬浮窗与本地统计',
        )}
      >
        <Card className="services-card">
          {snapshot.providers.map((provider) => (
            <ServiceRow
              key={provider.app}
              provider={provider}
              action={
                provider.app === 0
                  ? () =>
                      setCredits({
                        id: '',
                        name: settings.privacyMode ? 'Codex' : (provider.account?.email ?? 'Codex'),
                      })
                  : provider.app === 1
                    ? () => navigate('claude-accounts')
                    : provider.app === 4
                      ? () => setCommandKey(true)
                      : undefined
              }
            />
          ))}
          {[3, 4, 5, 6].map((app) => {
            const detected = snapshot.localServices[String(app)]
            return (
              <div className="service-row" key={app}>
                <Logo name={usageKeys[app] ?? ''} />
                <div className="service-identity">
                  <h3>
                    {usageNames[app]}
                    <span className="vendor">
                      {' '}
                      ·{' '}
                      {app === 3
                        ? 'pi.dev'
                        : app === 4
                          ? 'opencode.ai'
                          : app === 6
                            ? 'oh-my-pi'
                            : 'DeepSeek Harness'}
                    </span>
                  </h3>
                  <p className="ellipsis" title={detected?.source}>
                    {detected?.source ?? ''}
                  </p>
                </div>
                <div className="service-trailing">
                  <Status
                    kind={detected?.detected ? 'live' : 'muted'}
                    text={detected?.detected ? tr('Detected', '已检测到') : tr('Not detected', '未检测到')}
                  />
                  <Switch
                    checked={!!settings.usageVisibility[String(app)]}
                    disabled={busy}
                    label={usageNames[app] ?? ''}
                    onCheckedChange={(value) =>
                      update('usageVisibility', { ...settings.usageVisibility, [app]: value })
                    }
                  />
                </div>
                {settings.usageVisibility[String(app)] && (
                  <div className="destinations">
                    <span>{tr('Display', '展示位置')}</span>
                    <label>
                      <input type="checkbox" checked={false} disabled />
                      {tr('Tray', '通知区域')}
                    </label>
                    <label>
                      <input type="checkbox" checked={false} disabled />
                      {tr('Floating HUD', '悬浮窗')}
                    </label>
                    <label>
                      <input
                        type="checkbox"
                        checked
                        onChange={(event) =>
                          update('usageVisibility', {
                            ...settings.usageVisibility,
                            [app]: event.target.checked,
                          })
                        }
                      />
                      {tr('Usage stats', '用量统计')}
                    </label>
                  </div>
                )}
              </div>
            )
          })}
        </Card>
      </Section>
      <Section
        title={tr('Other Codex accounts', '其他 Codex 账号')}
        description={tr('Monitor extra accounts by importing auth.json.', '导入 auth.json 查看其他账号额度')}
        actions={
          <Button onClick={() => setCodexImport(true)}>
            <Plus size={14} />
            {tr('Add account', '添加账号')}
          </Button>
        }
      >
        <Card className="imported-card">
          {accounts.length === 0 && (
            <Empty
              title={tr('No additional accounts', '暂无其他账号')}
              description={tr('Paste a Codex auth.json to add one.', '粘贴 Codex auth.json 即可添加')}
            />
          )}
          {accounts.map((account, index) => (
            <div
              className="imported-row"
              key={account.id}
              onDragOver={(event) => event.preventDefault()}
              onDrop={(event) => {
                event.preventDefault()
                if (dragging) reorder(dragging, account.id)
                setDragging(undefined)
              }}
            >
              <div className="reorder-controls">
                <button
                  draggable
                  onDragStart={() => setDragging(account.id)}
                  onDragEnd={() => setDragging(undefined)}
                  className="drag-handle"
                  aria-label={tr('Drag to reorder', '拖动排序')}
                >
                  ⠿
                </button>
                <Button
                  tone="ghost"
                  className="tiny-button"
                  disabled={index === 0 || busy}
                  aria-label={tr('Move up', '上移')}
                  onClick={() => {
                    const before = accounts[index - 1]
                    if (before) reorder(account.id, before.id)
                  }}
                >
                  <ArrowUp size={12} />
                </Button>
                <Button
                  tone="ghost"
                  className="tiny-button"
                  disabled={index === accounts.length - 1 || busy}
                  aria-label={tr('Move down', '下移')}
                  onClick={() => {
                    const after = accounts[index + 1]
                    if (after) reorder(account.id, after.id)
                  }}
                >
                  <ArrowDown size={12} />
                </Button>
              </div>
              <div className="imported-identity">
                <h3 className="ellipsis" title={settings.privacyMode ? undefined : account.displayName}>
                  {settings.privacyMode ? tr('Codex account', 'Codex 账号') : account.displayName}
                </h3>
                <p className="ellipsis">
                  {settings.privacyMode ? '••••@••••' : account.email}
                  {account.planType ? ` · ${account.planType}` : ''}
                </p>
                {account.error && <p className="error-text">{account.error}</p>}
              </div>
              <div className="imported-actions">
                <label className="inline-toggle">
                  <span>{tr('Popover', '弹窗显示')}</span>
                  <Switch
                    checked={account.visibleInPopover}
                    disabled={busy}
                    label={tr('Show in popover', '在弹窗显示')}
                    onCheckedChange={(value) => {
                      void run(() =>
                        api.saveCodex(
                          accounts.map((item) =>
                            item.id === account.id ? { ...item, visibleInPopover: value } : item,
                          ),
                        ),
                      )
                    }}
                  />
                </label>
                <ActionMenu label={tr('Account actions', '账号操作')}>
                  <MenuItem
                    onSelect={() =>
                      setCredits({
                        id: account.id,
                        name: settings.privacyMode ? tr('Codex account', 'Codex 账号') : account.displayName,
                      })
                    }
                  >
                    {tr('Reset credits', '使用限额重置')}
                  </MenuItem>
                  <MenuItem danger onSelect={() => setRemove(account)}>
                    {tr('Remove', '移除')}
                  </MenuItem>
                </ActionMenu>
              </div>
              {account.snapshot && (
                <div className="imported-quotas">
                  <Quota limit={account.snapshot.primaryLimit} title={tr('5-hour', '5 小时')} compact />
                  <Quota limit={account.snapshot.secondaryLimit} title={tr('Weekly', '周额度')} compact />
                </div>
              )}
            </div>
          ))}
        </Card>
      </Section>
      {codexImport && <CodexImport close={() => setCodexImport(false)} />}
      {credits && <ResetCredits id={credits.id} name={credits.name} close={() => setCredits(undefined)} />}
      {commandKey && <CommandKey close={() => setCommandKey(false)} />}
      <Dialog
        open={!!remove}
        onOpenChange={(open) => {
          if (!open) setRemove(undefined)
        }}
        title={tr('Remove Codex account', '移除 Codex 账号')}
        description={tr('This account will be removed from CCBar.', '从 CCBar 列表移除此账号')}
        footer={
          <>
            <Button onClick={() => setRemove(undefined)}>{tr('Cancel', '取消')}</Button>
            <Button
              tone="danger"
              disabled={busy}
              onClick={async () => {
                if (remove && (await run(() => api.removeCodex(remove.id)))) setRemove(undefined)
              }}
            >
              {tr('Remove', '移除')}
            </Button>
          </>
        }
      >
        <p>{settings.privacyMode ? tr('Codex account', 'Codex 账号') : remove?.displayName}</p>
      </Dialog>
    </>
  )
}

function ServiceRow({ provider, action }: { provider: Provider; action?: () => void }) {
  const { snapshot, tr, busy, save } = useApp()
  const settings = snapshot.settings,
    display = settings.providers[String(provider.app)]
  if (!display) return null
  const usage = provider.app === 3 ? 2 : provider.app <= 1 ? provider.app : undefined
  const update = (value: Partial<typeof display>) => {
    void save({ ...settings, providers: { ...settings.providers, [provider.app]: { ...display, ...value } } })
  }
  const identity =
    provider.account?.email ??
    provider.account?.login ??
    provider.account?.accountUuid ??
    provider.account?.accountId ??
    tr('Not detected', '未检测到')
  const plan = provider.snapshot?.planType ?? provider.account?.subscriptionType
  const label =
    provider.app === 0
      ? tr('Reset credits', '使用限额重置')
      : provider.app === 1
        ? tr('Manage accounts', '管理账号')
        : tr('Credentials', '凭据设置')
  return (
    <div className="service-row">
      <Logo name={quotaKeys[provider.app] ?? ''} />
      <div className="service-identity">
        <h3>
          {provider.name}
          <span className="vendor">
            {' '}
            · {['OpenAI', 'Anthropic', 'Google', 'Cursor', 'Command Code'][provider.app]}
          </span>
        </h3>
        <p
          className="ellipsis"
          title={settings.privacyMode && provider.account?.email ? undefined : identity}
        >
          {settings.privacyMode && provider.account?.email ? '••••@••••' : identity}
          {plan ? ` · ${plan}` : ''}
        </p>
        {provider.error && <p className="error-text">{provider.error}</p>}
      </div>
      <div className="service-trailing">
        <Status
          kind={provider.account ? 'live' : 'muted'}
          text={provider.account ? tr('Connected', '已连接') : tr('Not detected', '未检测到')}
        />
        {action && (
          <Button tone="ghost" className="icon-button" aria-label={label} title={label} onClick={action}>
            {provider.app === 0 ? (
              <Sparkles size={15} />
            ) : provider.app === 1 ? (
              <ArrowRightLeft size={15} />
            ) : (
              <KeyRound size={15} />
            )}
          </Button>
        )}
        <Switch
          checked={display.enabled}
          disabled={busy}
          label={provider.name}
          onCheckedChange={(value) => update({ enabled: value })}
        />
      </div>
      {display.enabled && (
        <div className="destinations">
          <span>{tr('Display', '展示位置')}</span>
          <label>
            <input
              type="checkbox"
              checked={display.menuBar}
              disabled={busy}
              onChange={(event) => update({ menuBar: event.target.checked })}
            />
            {tr('Tray', '通知区域')}
          </label>
          <label
            title={
              !settings.floatingEnabled
                ? tr('Enable the floating HUD in Appearance & Display.', '在「外观与显示」开启悬浮窗')
                : undefined
            }
          >
            <input
              type="checkbox"
              checked={display.floatingHud}
              disabled={busy || !settings.floatingEnabled}
              onChange={(event) => update({ floatingHud: event.target.checked })}
            />
            {tr('Floating HUD', '悬浮窗')}
          </label>
          {usage !== undefined && (
            <label>
              <input
                type="checkbox"
                checked={!!settings.usageVisibility[String(usage)]}
                disabled={busy}
                onChange={(event) => {
                  void save({
                    ...settings,
                    usageVisibility: { ...settings.usageVisibility, [usage]: event.target.checked },
                  })
                }}
              />
              {tr('Usage stats', '用量统计')}
            </label>
          )}
        </div>
      )}
    </div>
  )
}

function CodexImport({ close }: { close: () => void }) {
  const { tr, busy, run } = useApp()
  const [text, setText] = useState(''),
    [previewText, setPreviewText] = useState(''),
    [visible, setVisible] = useState(true)
  const result = useQuery(previewText, () => api.previewCodex(previewText), !!previewText)
  const submit = async () => {
    if (await run(() => api.importCodex(text, visible), tr('Account imported', '账号已导入'))) close()
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) close()
      }}
      title={tr('Import Codex account', '导入 Codex 账号')}
      description={tr(
        'Paste auth.json, exported accounts, or a personal access token.',
        '粘贴 auth.json、导出的账号或个人访问令牌',
      )}
      footer={
        <>
          <Button onClick={close}>{tr('Cancel', '取消')}</Button>
          <Button tone="primary" disabled={busy || !text.trim()} onClick={submit}>
            {tr('Import', '导入')}
          </Button>
        </>
      }
    >
      <label className="field-label">
        {tr('Account credentials', '账号凭据')}
        <textarea
          className="credential-input"
          value={text}
          onChange={(event) => setText(event.target.value)}
          onBlur={() => setPreviewText(text.trim())}
          autoFocus
          spellCheck={false}
        />
      </label>
      {previewText && (
        <>
          <QueryState pending={result.pending} error={result.error} />
          {result.data?.map((account, index) => (
            <div className="import-preview" key={account.accountId ?? index}>
              {account.email ?? account.accountId ?? tr('Personal access token', '个人访问令牌')}
              <span>{account.plan}</span>
            </div>
          ))}
        </>
      )}
      <label className="inline-toggle">
        <span>{tr('Show in popover', '在弹出面板中显示')}</span>
        <Switch
          checked={visible}
          onCheckedChange={setVisible}
          label={tr('Show in popover', '在弹出面板中显示')}
        />
      </label>
    </Dialog>
  )
}

function CommandKey({ close }: { close: () => void }) {
  const { snapshot, tr, save, run, busy } = useApp()
  const [source, setSource] = useState(snapshot.settings.commandCodeCredentialPreference),
    [key, setKey] = useState('')
  const submit = async () => {
    if (key.trim() && !(await run(() => api.commandKey(key.trim())))) return
    if (await save({ ...snapshot.settings, commandCodeCredentialPreference: source })) close()
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) close()
      }}
      title={tr('Command Code credentials', 'Command Code 凭据设置')}
      footer={
        <>
          <Button onClick={close}>{tr('Cancel', '取消')}</Button>
          <Button tone="primary" disabled={busy} onClick={submit}>
            {tr('Save', '保存')}
          </Button>
        </>
      }
    >
      <label className="field-label">
        {tr('Credential source', '凭据来源')}
        <Select
          disabled={busy}
          value={source}
          onChange={setSource}
          label={tr('Credential source', '凭据来源')}
          options={[
            { value: 'automatic', label: tr('Automatic', '自动读取') },
            { value: 'manual', label: tr('Manual API key', '手动 API Key') },
          ]}
        />
      </label>
      {source === 'manual' && (
        <>
          <label className="field-label">
            API Key
            <input
              type="password"
              autoComplete="off"
              value={key}
              onChange={(event) => setKey(event.target.value)}
              placeholder={tr('Enter a new key to replace the saved key', '输入新 Key 以替换已保存凭据')}
            />
          </label>
          <Button
            tone="danger"
            disabled={busy}
            onClick={() => run(api.clearCommandKey, tr('Saved key removed', '已保存的 Key 已移除'))}
          >
            {tr('Remove saved key', '删除已保存 Key')}
          </Button>
        </>
      )}
      {source === 'automatic' && (
        <p className="secondary">
          {snapshot.providers.find((provider) => provider.app === 4)?.account?.source ??
            tr('Command Code local credentials', 'Command Code 本机凭据')}
        </p>
      )}
    </Dialog>
  )
}

function Appearance() {
  const { snapshot, tr, save, busy } = useApp(),
    settings = snapshot.settings
  const set = <K extends keyof SettingsModel>(key: K, value: SettingsModel[K]) => {
    void save({ ...settings, [key]: value })
  }
  return (
    <>
      <Section title={tr('Appearance', '外观')}>
        <Card>
          <SettingRow title={tr('Theme', '主题')}>
            <Segments
              label={tr('Theme', '主题')}
              value={settings.theme}
              onChange={(value) => {
                if (value === 'system' || value === 'light' || value === 'dark') set('theme', value)
              }}
              options={[
                { value: 'system', label: tr('System', '跟随系统') },
                { value: 'light', label: tr('Light', '浅色') },
                { value: 'dark', label: tr('Dark', '深色') },
              ]}
            />
          </SettingRow>
        </Card>
      </Section>
      <Section title={tr('Tray', '通知区域')}>
        <Card>
          <SettingRow
            title={tr('Quota period', '额度周期')}
            description={tr(
              'Choose the quota window shown in the tray tooltip.',
              '选择通知区域提示显示的额度窗口',
            )}
          >
            <Select
              disabled={busy}
              value={settings.menuBarWindow}
              onChange={(value) => set('menuBarWindow', value)}
              label={tr('Quota period', '额度周期')}
              options={[
                { value: 'primary', label: tr('Main', '主要额度') },
                { value: 'weekly', label: tr('Weekly', '周额度') },
                { value: 'both', label: tr('Both', '都显示') },
              ]}
            />
          </SettingRow>
        </Card>
      </Section>
      <Section
        title={tr('Floating HUD', '桌面悬浮窗')}
        description={tr('A compact always-on-top quota window.', '桌面常驻的紧凑额度窗口')}
      >
        <Card>
          <SettingRow
            title={tr('Show floating window', '显示悬浮窗')}
            description={tr(
              'Choose individual services in Services & Accounts.',
              '在「服务与账号」选择各服务的悬浮窗展示',
            )}
          >
            <Switch
              checked={settings.floatingEnabled}
              disabled={busy}
              onCheckedChange={(value) => set('floatingEnabled', value)}
              label={tr('Show floating window', '显示悬浮窗')}
            />
          </SettingRow>
        </Card>
      </Section>
      <Section title={tr('Display details', '显示细节')}>
        <Card>
          <SettingRow
            title={tr('Reset time', '重置时间')}
            description={tr(
              'How quota reset time appears in the popover.',
              '弹出窗口中额度重置时间的显示方式',
            )}
          >
            <Select
              disabled={busy}
              value={settings.resetTimeDisplay}
              onChange={(value) => set('resetTimeDisplay', value)}
              label={tr('Reset time', '重置时间')}
              options={[
                { value: 'relative', label: tr('Remaining time', '剩余时长') },
                { value: 'absolute', label: tr('Exact time', '具体时间') },
              ]}
            />
          </SettingRow>
          <SettingRow
            title={tr('Service status dots', '服务状态圆点')}
            description={tr(
              'Show the official service status next to each provider.',
              '为每个服务显示官方状态页圆点',
            )}
          >
            <Switch
              checked={settings.showServiceStatus}
              disabled={busy}
              onCheckedChange={(value) => set('showServiceStatus', value)}
              label={tr('Service status dots', '服务状态圆点')}
            />
          </SettingRow>
          <SettingRow
            title={tr('Privacy mode', '隐私模式')}
            description={tr('Hide account emails and saved account names.', '隐藏账号邮箱与已保存账号名称')}
          >
            <Switch
              checked={settings.privacyMode}
              disabled={busy}
              onCheckedChange={(value) => set('privacyMode', value)}
              label={tr('Privacy mode', '隐私模式')}
            />
          </SettingRow>
        </Card>
      </Section>
    </>
  )
}

function Data() {
  const { snapshot, tr, english, save, busy, run } = useApp(),
    settings = snapshot.settings
  const [confirmRebuild, setConfirmRebuild] = useState(false)
  const intervals = [1, 2, 3, 5, 10].map((value) => ({
    value: String(value),
    label: `${value} ${tr('min', '分钟')}`,
  }))
  const last = snapshot.providers
    .map((provider) => provider.lastSuccessAt)
    .filter((value): value is string => !!value)
    .sort()
    .at(-1)
  return (
    <>
      <Section
        title={tr('Polling intervals', '后台刷新')}
        description={tr('Background quota polling and local log scanning.', '后台轮询额度与本地日志扫描')}
      >
        <Card>
          <SettingRow title={tr('Quota refresh', '额度刷新')}>
            <Select
              disabled={busy}
              value={String(settings.quotaIntervalMinutes)}
              label={tr('Quota refresh', '额度刷新')}
              options={intervals}
              onChange={(value) => {
                void save({ ...settings, quotaIntervalMinutes: Number(value) })
              }}
            />
          </SettingRow>
          <SettingRow title={tr('Log scan', '日志扫描')}>
            <Select
              disabled={busy}
              value={String(settings.usageIntervalMinutes)}
              label={tr('Log scan', '日志扫描')}
              options={intervals}
              onChange={(value) => {
                void save({ ...settings, usageIntervalMinutes: Number(value) })
              }}
            />
          </SettingRow>
          <SettingRow title={tr('Last refresh', '上次刷新')}>
            <span className="secondary numeric">{dateTime(last, english, true)}</span>
          </SettingRow>
        </Card>
      </Section>
      <Section title={tr('Data maintenance', '数据维护')}>
        <Card>
          <SettingRow
            title={tr('Price catalog', '价格目录')}
            description={tr(
              'Update Standard and Fast pricing. Recalculate to reprice historical records.',
              '更新 Standard 与 Fast 价格；重新计算可更新历史记录费用',
            )}
          >
            <Button disabled={busy} onClick={() => run(api.prices, tr('Prices updated', '价格目录已更新'))}>
              <RefreshCw size={14} />
              {tr('Update', '更新')}
            </Button>
          </SettingRow>
          <SettingRow
            title={tr('Recalculate usage', '重新计算用量')}
            description={tr(
              'Rescan local logs and compute historical costs using the current catalog.',
              '扫描全部本地日志，按当前价格目录重新计算历史费用',
            )}
          >
            <Button disabled={busy || snapshot.scan.isScanning} onClick={() => setConfirmRebuild(true)}>
              {snapshot.scan.isScanning ? tr('Scanning…', '扫描中…') : tr('Recalculate', '重新计算')}
            </Button>
          </SettingRow>
          {snapshot.scan.error && <p className="inline-error">{snapshot.scan.error}</p>}
        </Card>
      </Section>
      <Dialog
        open={confirmRebuild}
        onOpenChange={setConfirmRebuild}
        title={tr('Recalculate historical usage?', '重新计算历史用量？')}
        description={tr(
          'All local records will be rescanned and repriced.',
          '将重新扫描全部本地记录并计算费用',
        )}
        footer={
          <>
            <Button onClick={() => setConfirmRebuild(false)}>{tr('Cancel', '取消')}</Button>
            <Button
              tone="primary"
              disabled={busy}
              onClick={async () => {
                if (await run(() => api.scan(true), tr('Usage recalculated', '用量已重新计算')))
                  setConfirmRebuild(false)
              }}
            >
              {tr('Recalculate', '重新计算')}
            </Button>
          </>
        }
      >
        {snapshot.scan.filesTotal !== undefined && (
          <p>
            {snapshot.scan.filesCompleted ?? 0} / {snapshot.scan.filesTotal}
          </p>
        )}
      </Dialog>
    </>
  )
}

function General() {
  const { snapshot, tr, busy, save, run } = useApp(),
    settings = snapshot.settings
  const [diagnostics, setDiagnostics] = useState(false)
  return (
    <>
      <Section title={tr('System', '系统')}>
        <Card>
          <SettingRow title={tr('Language', '语言')}>
            <Select
              disabled={busy}
              value={settings.language}
              label={tr('Language', '语言')}
              onChange={(value) => {
                void save({ ...settings, language: value })
              }}
              options={[
                { value: 'system', label: tr('Follow system', '跟随系统') },
                { value: 'zh', label: '中文' },
                { value: 'en', label: 'English' },
              ]}
            />
          </SettingRow>
          <SettingRow title={tr('Launch at login', '开机自动启动')}>
            <Switch
              checked={settings.launchAtLogin}
              disabled={busy}
              label={tr('Launch at login', '开机自动启动')}
              onCheckedChange={(value) => {
                void save({ ...settings, launchAtLogin: value })
              }}
            />
          </SettingRow>
        </Card>
      </Section>
      <Section title={tr('Diagnostics', '诊断')}>
        <Card>
          <SettingRow
            title={tr('Export diagnostics', '导出诊断日志')}
            description={tr(
              'Bundle redacted logs and app state into a local ZIP file.',
              '将脱敏日志与运行状态打包为本地 ZIP 文件',
            )}
          >
            <Button disabled={busy} onClick={() => setDiagnostics(true)}>
              {tr('Export', '导出')}
            </Button>
          </SettingRow>
          <SettingRow
            title={tr('Open data directory', '打开数据目录')}
            description={tr('Settings, usage database and application logs.', '设置、用量数据库与应用日志')}
          >
            <Button disabled={busy} onClick={() => run(api.dataDirectory)}>
              <FolderOpen size={14} />
              {tr('Open', '打开')}
            </Button>
          </SettingRow>
          <SettingRow
            title={tr('Verbose logging', '详细日志')}
            description={tr('Record additional details for troubleshooting.', '记录更详细的排查信息')}
          >
            <Switch
              checked={settings.verboseLogging}
              disabled={busy}
              label={tr('Verbose logging', '详细日志')}
              onCheckedChange={(value) => {
                void save({ ...settings, verboseLogging: value })
              }}
            />
          </SettingRow>
        </Card>
      </Section>
      <Updates />
      <Dialog
        open={diagnostics}
        onOpenChange={setDiagnostics}
        title={tr('Export diagnostics?', '导出诊断日志？')}
        description={tr(
          'Includes app version, Windows version, settings, provider status, redacted logs and scan statistics. Credentials and conversation content are excluded.',
          '包含版本、Windows 信息、设置、服务状态、脱敏日志与扫描统计；凭据和对话正文不纳入',
        )}
        footer={
          <>
            <Button onClick={() => setDiagnostics(false)}>{tr('Cancel', '取消')}</Button>
            <Button
              tone="primary"
              disabled={busy}
              onClick={async () => {
                let path = ''
                if (
                  await run(
                    async () => {
                      path = await api.diagnostics()
                    },
                    tr('Diagnostics exported', '诊断日志已导出'),
                  )
                )
                  setDiagnostics(false)
                if (path) void run(api.dataDirectory)
              }}
            >
              {tr('Export', '导出')}
            </Button>
          </>
        }
      >
        <p className="secondary">{tr('The ZIP file is saved on this computer.', 'ZIP 文件保存在本机')}</p>
      </Dialog>
    </>
  )
}

// Updates 显示正式版本检查结果并确认原位安装
function Updates() {
  const { snapshot, tr } = useApp()
  const [release, setRelease] = useState<ReleaseUpdate>()
  const [working, setWorking] = useState<'checking' | 'installing' | 'restarting'>()
  const [error, setError] = useState('')
  const [confirm, setConfirm] = useState(false)
  const check = async () => {
    setWorking('checking')
    setError('')
    try {
      const result = await api.checkUpdates()
      setRelease(result)
      if (result.installError) setError(result.installError)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      setWorking(undefined)
    }
  }
  const install = async () => {
    setWorking('installing')
    setError('')
    try {
      await api.installUpdate()
      setWorking('restarting')
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause))
      setWorking(undefined)
      setConfirm(false)
    }
  }
  const title =
    working === 'checking'
      ? tr('Checking GitHub…', '正在检查 GitHub…')
      : release?.status === 'available'
        ? tr(`Version ${release.latestVersion} is available`, `发现新版本 ${release.latestVersion}`)
        : release?.status === 'current'
          ? tr('Up to date', '已是最新版本')
          : release?.status === 'unpublished'
            ? tr('No Windows release published yet', '尚未发布 Windows 版本')
            : tr('Check available versions', '检查可用版本')
  return (
    <Section title={tr('Updates', '更新')}>
      <Card>
        <SettingRow
          title={title}
          description={tr(`Current version ${snapshot.version}`, `当前版本 ${snapshot.version}`)}
        >
          <Button disabled={!!working} onClick={() => void check()}>
            {working === 'checking' ? <LoaderCircle size={14} className="spin" /> : <RefreshCw size={14} />}
            {tr('Check for updates', '检查更新')}
          </Button>
        </SettingRow>
        {release?.status === 'available' && (
          <SettingRow
            title={tr(`Install ${release.latestVersion}`, `安装 ${release.latestVersion}`)}
            description={tr('Restart CCBar after downloading.', '下载完成后重启 CCBar')}
          >
            <Button tone="primary" disabled={!!working} onClick={() => setConfirm(true)}>
              <Download size={14} />
              {tr('Download & install', '下载并安装')}
            </Button>
          </SettingRow>
        )}
        {error && (
          <p className="inline-error" role="alert">
            {error}
          </p>
        )}
      </Card>
      <Dialog
        open={confirm}
        onOpenChange={(open) => {
          if (!working) setConfirm(open)
        }}
        title={tr(`Install CCBar ${release?.latestVersion}?`, `安装 CCBar ${release?.latestVersion}？`)}
        description={tr(
          'Download the Windows release from Mag1cFall/cc-bar, replace this EXE and restart. Settings and history stay in the data directory.',
          '从 Mag1cFall/cc-bar 下载 Windows 版本，替换当前 EXE 并重启。设置与记录继续保存在数据目录中',
        )}
        footer={
          <>
            <Button disabled={!!working} onClick={() => setConfirm(false)}>
              {tr('Cancel', '取消')}
            </Button>
            <Button tone="primary" disabled={!!working} onClick={() => void install()}>
              {working && <LoaderCircle size={14} className="spin" />}
              {working === 'installing'
                ? tr('Downloading & installing…', '正在下载并安装…')
                : working === 'restarting'
                  ? tr('Restarting…', '正在重启…')
                  : tr('Install & restart', '安装并重启')}
            </Button>
          </>
        }
      >
        {release?.notes && (
          <p className="secondary" style={{ whiteSpace: 'pre-wrap' }}>
            {release.notes}
          </p>
        )}
      </Dialog>
    </Section>
  )
}
