import { useState } from 'react'
import {
  Plus,
  RefreshCw,
  UserRound,
  Play,
  ArrowRightLeft,
  Download,
  LoaderCircle,
  Check,
  ExternalLink,
  AlertCircle,
  X,
} from 'lucide-react'
import { api } from './bridge'
import { useApp } from './context'
import { ActionMenu, Button, Card, Dialog, Empty, Logo, MenuItem, Quota, Section, Status } from './components'
import type { ClaudeAccount } from './models'
import { dateTime } from './format'

// Accounts 展示当前登录与已保存账号
export default function Accounts() {
  const { snapshot, tr, busy, run } = useApp()
  const [dialog, setDialog] = useState<{ type: 'rename' | 'remove'; account: ClaudeAccount }>()
  const [name, setName] = useState('')
  const [saving, setSaving] = useState(false)
  const [dismissedLogin, setDismissedLogin] = useState<string>()
  const accounts = snapshot.claudeAccounts ?? []
  const login = snapshot.claudeLogin
  const signingIn = !!login && !['done', 'error', 'cancelled'].includes(login.stage)
  const current =
    accounts.find((account) => account.desktopActive) ?? accounts.find((account) => account.isActive)
  const openRename = (account: ClaudeAccount) => {
    setName(account.name)
    setDialog({ type: 'rename', account })
  }
  const submit = async () => {
    if (busy || !dialog) return
    const account = dialog.account
    const ok = await run(
      async () => {
        if (dialog.type === 'remove' && account) {
          await api.removeClaude(account.id)
        } else if (dialog.type === 'rename' && account) {
          await api.renameClaude(account.id, name.trim())
        }
      },
      dialog.type === 'remove' ? tr('Account removed', '账号已移除') : undefined,
    )
    if (ok) setDialog(undefined)
  }
  return (
    <div className="settings-content accounts-page">
      <header className="page-heading">
        <div className="page-title">
          <Logo name="claude" size={42} />
          <div>
            <h1>{tr('Claude Accounts', 'Claude 账号')}</h1>
            <p>{tr('Manage sign-ins and switch accounts', '管理登录与切换账号')}</p>
          </div>
        </div>
        <div className="actions">
          <Button
            disabled={busy || signingIn}
            onClick={async () => {
              setSaving(true)
              await run(api.saveClaude, tr('Sign-in saved', '当前登录已保存'))
              setSaving(false)
            }}
          >
            {saving ? <LoaderCircle size={15} className="spin" /> : <Download size={15} />}
            {tr('Save current sign-in', '保存当前登录')}
          </Button>
          <Button tone="primary" disabled={busy || signingIn} onClick={() => run(() => api.loginClaude())}>
            <Plus size={15} />
            {tr('Add account', '添加账号')}
          </Button>
        </div>
      </header>
      <p className="page-note">
        {tr(
          'Switching reopens Desktop with the selected Chat, Cowork and Code account. Local Code history is shared.',
          '切换时 Desktop 会以所选账号重新打开 Chat、Cowork 和 Code，本地 Code 对话记录共享',
        )}
      </p>
      {login && login.id !== dismissedLogin && <LoginProgress dismiss={() => setDismissedLogin(login.id)} />}
      <Section title={tr('Current account', '当前账号')}>
        {current ? (
          <AccountCard
            account={current}
            rename={openRename}
            remove={(account) => setDialog({ type: 'remove', account })}
          />
        ) : (
          <Card>
            <Empty
              title={tr('Choose a default account', '选择默认账号')}
              description={tr(
                'Add an account or save an existing Desktop or Code sign-in.',
                '添加账号，或保存已有 Desktop、Code 登录',
              )}
            />
          </Card>
        )}
      </Section>
      <Section title={tr('Saved accounts', '已保存账号')}>
        <div className="account-list">
          {accounts
            .filter((account) => account.id !== current?.id)
            .map((account) => (
              <AccountCard
                key={account.id}
                account={account}
                rename={openRename}
                remove={(selected) => setDialog({ type: 'remove', account: selected })}
              />
            ))}
          {accounts.filter((account) => account.id !== current?.id).length === 0 && (
            <Card>
              <Empty title={tr('No other accounts yet', '暂无其他账号')} />
            </Card>
          )}
        </div>
      </Section>
      <footer className="account-footer">
        <span>
          {accounts.length} {tr('accounts', '个账号')}
        </span>
        <Button tone="ghost" disabled={busy || signingIn} onClick={() => run(api.refreshClaude)}>
          <RefreshCw size={14} />
          {tr('Refresh quotas', '刷新额度')}
        </Button>
      </footer>
      <Dialog
        open={!!dialog}
        onOpenChange={(open) => {
          if (!open) setDialog(undefined)
        }}
        title={
          dialog?.type === 'rename' ? tr('Rename account', '重命名账号') : tr('Remove account', '移除账号')
        }
        description={
          dialog?.type === 'remove'
            ? tr(
                'Remove this account from the list. Its local sign-in and conversations are kept.',
                '从列表移除此账号，本机登录与对话记录保留',
              )
            : undefined
        }
        footer={
          <>
            <Button onClick={() => setDialog(undefined)}>{tr('Cancel', '取消')}</Button>
            <Button
              tone={dialog?.type === 'remove' ? 'danger' : 'primary'}
              disabled={busy || (dialog?.type !== 'remove' && name.trim().length === 0)}
              onClick={submit}
            >
              {dialog?.type === 'remove' ? tr('Remove', '移除') : tr('Save', '保存')}
            </Button>
          </>
        }
      >
        {dialog?.type === 'remove' ? (
          <p className="confirm-name">
            {snapshot.settings.privacyMode ? tr('Claude account', 'Claude 账号') : dialog.account?.name}
          </p>
        ) : (
          <label className="field-label">
            {tr('Account name', '账号名称')}
            <input
              autoFocus
              value={name}
              onChange={(event) => setName(event.target.value)}
              maxLength={100}
              onKeyDown={(event) => {
                if (event.key === 'Enter' && name.trim()) void submit()
              }}
            />
          </label>
        )}
      </Dialog>
    </div>
  )
}

// LoginProgress 展示原生授权与同步切换的真实阶段
function LoginProgress({ dismiss }: { dismiss: () => void }) {
  const { snapshot, tr, run } = useApp()
  const login = snapshot.claudeLogin!
  const finished = ['done', 'error', 'cancelled'].includes(login.stage)
  const [cancellingId, setCancellingId] = useState<string>()
  const cancelling = !finished && (login.stage === 'cancelling' || cancellingId === login.id)
  const steps =
    login.mode === 'switch'
      ? [
          tr('Save current sign-in', '保存当前登录'),
          tr('Load account', '载入账号'),
          tr('Reload Desktop', '重新加载 Desktop'),
        ]
      : login.mode === 'save'
        ? [
            tr('Connect Code', '连接 Code'),
            tr('Save sign-in', '保存登录'),
            tr('Reload Desktop', '重新加载 Desktop'),
          ]
        : [
            tr('Prepare sign-in', '准备登录'),
            tr('Official sign-in', '官方授权'),
            tr('Connect Code', '连接 Code'),
            tr('Save account', '保存账号'),
          ]
  const stageIndex: Record<string, number> = {
    starting: 0,
    closing: 0,
    browser: 1,
    exchanging: 2,
    linking: 2,
    verifying: 2,
    persisting: 3,
    saving: 3,
    restoring: 1,
    reloading: 2,
    done: steps.length,
  }
  const current =
    login.mode === 'save'
      ? ({ starting: 0, linking: 0, closing: 1, saving: 1, reloading: 2, done: 3 }[login.stage] ?? 0)
      : (stageIndex[login.stage] ?? 0)
  const title =
    login.stage === 'done'
      ? tr('Account ready', '账号已就绪')
      : login.stage === 'error'
        ? login.mode === 'switch'
          ? tr('Switch failed', '切换失败')
          : tr('Sign-in failed', '登录失败')
        : login.stage === 'cancelled'
          ? tr('Sign-in cancelled', '登录已取消')
          : cancelling
            ? tr('Restoring the previous sign-in…', '正在恢复此前登录…')
            : steps[Math.min(current, steps.length - 1)]
  return (
    <Card className="login-progress">
      <div className="login-progress-heading" role="status" aria-live="polite">
        {login.stage === 'done' ? (
          <Check size={18} className="positive-text" />
        ) : login.stage === 'error' ? (
          <AlertCircle size={18} className="error-text" />
        ) : finished ? (
          <X size={18} />
        ) : (
          <LoaderCircle size={18} className="spin" />
        )}
        <strong>{title}</strong>
        {!finished && (
          <Button
            tone="ghost"
            disabled={cancelling}
            onClick={async () => {
              setCancellingId(login.id)
              if (!(await run(api.cancelClaudeLogin))) setCancellingId(undefined)
            }}
          >
            {tr('Cancel', '取消')}
          </Button>
        )}
        {login.stage === 'error' && login.mode !== 'switch' && login.mode !== 'save' && (
          <Button onClick={() => run(() => api.loginClaude(login.accountId))}>{tr('Retry', '重试')}</Button>
        )}
        {finished && (
          <Button tone="ghost" aria-label={tr('Dismiss', '关闭提示')} onClick={dismiss}>
            <X size={16} />
          </Button>
        )}
      </div>
      {!finished && (
        <ol className="login-steps">
          {steps.map((label, index) => (
            <li key={label} className={index < current ? 'completed' : index === current ? 'active' : ''}>
              <span>{index < current ? <Check size={12} /> : index + 1}</span>
              {label}
            </li>
          ))}
        </ol>
      )}
      {login.stage === 'browser' && (
        <p className="secondary">
          {login.mode === 'desktop'
            ? tr(
                'Complete sign-in in Claude Desktop. Desktop will reopen for this account.',
                '在 Claude Desktop 完成登录，Desktop 将为此账号重新打开',
              )
            : tr(
                'Complete authorization in your browser and return automatically.',
                '在浏览器完成授权后将自动返回',
              )}{' '}
        </p>
      )}
      {login.stage === 'persisting' && (
        <p className="secondary">
          {tr('Signed in. Saving the Desktop session…', '登录已完成，正在保存 Desktop 会话…')}
        </p>
      )}
      {login.error && (
        <p className="error-text" role="alert">
          {login.error}
        </p>
      )}
      {login.url && !finished && (
        <Button tone="ghost" className="authorization-link" onClick={() => run(api.openClaudeAuthorization)}>
          <ExternalLink size={13} />
          {tr('Open authorization page', '打开授权页面')}
        </Button>
      )}
    </Card>
  )
}

function AccountCard({
  account,
  rename,
  remove,
}: {
  account: ClaudeAccount
  rename: (account: ClaudeAccount) => void
  remove: (account: ClaudeAccount) => void
}) {
  const { snapshot, tr, english, run, busy } = useApp()
  const login = snapshot.claudeLogin
  const pending = busy || (!!login && !['done', 'error', 'cancelled'].includes(login.stage))
  const privacy = snapshot.settings.privacyMode
  const email = privacy ? '••••@••••' : (account.email ?? tr('Not signed in', '尚未登录'))
  const displayName = privacy ? tr('Claude account', 'Claude 账号') : account.name
  return (
    <Card className={`account-card ${account.isActive || account.desktopActive ? 'current-account' : ''}`}>
      <div className="account-heading">
        <div className="account-avatar">
          <UserRound size={22} />
        </div>
        <div className="account-identity">
          <div className="account-name">
            <h3 title={displayName}>{displayName}</h3>
            {account.isActive && <span className="badge live">{tr('CLI default', 'CLI 默认')}</span>}
            {account.desktopActive && <span className="badge live">Desktop</span>}
          </div>
          <p className="ellipsis" title={email}>
            {email}
            {account.plan ? ` · ${account.plan}` : ''}
          </p>
          {account.isLoggingIn ? (
            <Status kind="low" text={tr('Waiting for sign-in…', '等待登录完成…')} />
          ) : account.needsLogin ? (
            <Status kind="low" text={tr('Sign in again', '需要重新登录')} />
          ) : account.error ? (
            <p className="error-text">{account.error}</p>
          ) : (
            <Status kind="live" text={tr('Signed in', '已登录')} />
          )}
        </div>
        <div className="account-actions">
          {account.needsLogin && !account.desktopSaved ? (
            <Button
              disabled={pending || account.isLoggingIn}
              onClick={() => run(() => api.loginClaude(account.id))}
            >
              {tr('Sign in', '重新登录')}
            </Button>
          ) : (
            <>
              {(!account.isActive || (account.desktopSaved && !account.desktopActive)) && (
                <Button
                  disabled={pending || account.isLoggingIn}
                  onClick={() =>
                    run(() => api.switchClaude(account.id), tr('Account switched', '账号已切换'))
                  }
                >
                  <ArrowRightLeft size={14} />
                  {tr('Switch', '切换')}
                </Button>
              )}
              <Button
                disabled={pending || account.isLoggingIn || account.needsLogin}
                onClick={() => run(() => api.startClaude(account.id))}
              >
                <Play size={14} />
                {account.isActive ? tr('Start Claude Code', '启动 Claude Code') : tr('Start', '启动')}
              </Button>
            </>
          )}
          <ActionMenu label={tr('Account actions', '账号操作')}>
            <MenuItem disabled={pending} onSelect={() => rename(account)}>
              {tr('Rename', '重命名')}
            </MenuItem>
            <MenuItem
              disabled={pending || account.isLoggingIn}
              onSelect={() => {
                void run(() => api.loginClaude(account.id))
              }}
            >
              {tr('Sign in again', '重新登录')}
            </MenuItem>
            <MenuItem disabled={pending} danger onSelect={() => remove(account)}>
              {tr('Remove from list', '从列表移除')}
            </MenuItem>
          </ActionMenu>
        </div>
      </div>
      {account.snapshot && (
        <>
          <div className="account-quotas">
            <Quota limit={account.snapshot.primaryLimit} title={tr('5-hour remaining', '5 小时剩余')} />
            <Quota limit={account.snapshot.secondaryLimit} title={tr('Weekly remaining', '周额度剩余')} />
          </div>
          <div className="account-updated">
            {tr('Updated', '更新于')} {dateTime(account.snapshot.fetchedAt, english, true)}
          </div>
        </>
      )}
    </Card>
  )
}
