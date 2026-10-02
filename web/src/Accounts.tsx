import { useState } from 'react'
import { Plus, RefreshCw, UserRound, Play, ArrowRightLeft, Download } from 'lucide-react'
import { api } from './bridge'
import { useApp } from './context'
import { ActionMenu, Button, Card, Dialog, Empty, Logo, MenuItem, Quota, Section, Status } from './components'
import type { ClaudeAccount } from './models'
import { dateTime } from './format'

// Accounts 按设计稿独立呈现当前账号与可切换账号
export default function Accounts() {
  const { snapshot, tr, busy, run } = useApp()
  const [dialog, setDialog] = useState<{ type: 'add' | 'rename' | 'remove'; account?: ClaudeAccount }>()
  const [name, setName] = useState('')
  const accounts = snapshot.claudeAccounts ?? []
  const current = accounts.find((account) => account.isActive)
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
        } else {
          const created = await api.addClaude(name.trim())
          await api.loginClaude(created.id)
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
            <h1>{tr('Claude Code Accounts', 'Claude Code 账号')}</h1>
            <p>{tr('Manage sign-ins and switch accounts', '管理登录与切换账号')}</p>
          </div>
        </div>
        <div className="actions">
          <Button disabled={busy} onClick={() => run(api.saveClaude, tr('Sign-in saved', '当前登录已保存'))}>
            <Download size={15} />
            {tr('Save current sign-in', '保存当前登录')}
          </Button>
          <Button
            tone="primary"
            disabled={busy}
            onClick={() => {
              setName('')
              setDialog({ type: 'add' })
            }}
          >
            <Plus size={15} />
            {tr('Add account', '添加账号')}
          </Button>
        </div>
      </header>
      <p className="page-note">
        {tr(
          'New Claude Code sessions use the selected account. Conversation history is shared.',
          '新启动的 Claude Code 使用所选账号，对话记录共享',
        )}
      </p>
      <Section title={tr('Default CLI account', '默认 CLI 账号')}>
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
                'Save a Claude Code or Desktop Code sign-in, then select Switch on the saved account.',
                '保存 Claude Code 或 Desktop Code 登录后，在已保存账号中点击「切换」',
              )}
            />
          </Card>
        )}
      </Section>
      <Section title={tr('Saved accounts', '已保存账号')}>
        <div className="account-list">
          {accounts
            .filter((account) => !account.isActive)
            .map((account) => (
              <AccountCard
                key={account.id}
                account={account}
                rename={openRename}
                remove={(selected) => setDialog({ type: 'remove', account: selected })}
              />
            ))}
          {accounts.filter((account) => !account.isActive).length === 0 && (
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
        <Button tone="ghost" disabled={busy} onClick={() => run(api.refreshClaude)}>
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
          dialog?.type === 'add'
            ? tr('Add Claude account', '添加 Claude 账号')
            : dialog?.type === 'rename'
              ? tr('Rename account', '重命名账号')
              : tr('Remove account', '移除账号')
        }
        description={
          dialog?.type === 'add'
            ? tr(
                'Claude Code opens its official browser sign-in flow.',
                'Claude Code 将打开官方浏览器登录流程',
              )
            : dialog?.type === 'remove'
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
              {dialog?.type === 'remove'
                ? tr('Remove', '移除')
                : dialog?.type === 'add'
                  ? tr('Continue to sign in', '继续登录')
                  : tr('Save', '保存')}
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
  const privacy = snapshot.settings.privacyMode
  const email = privacy ? '••••@••••' : (account.email ?? tr('Not signed in', '尚未登录'))
  const displayName = privacy ? tr('Claude account', 'Claude 账号') : account.name
  return (
    <Card className={`account-card ${account.isActive ? 'current-account' : ''}`}>
      <div className="account-heading">
        <div className="account-avatar">
          <UserRound size={22} />
        </div>
        <div className="account-identity">
          <div className="account-name">
            <h3 title={displayName}>{displayName}</h3>
            {account.isActive && <span className="badge live">{tr('Current', '当前')}</span>}
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
          {account.needsLogin ? (
            <Button
              disabled={busy || account.isLoggingIn}
              onClick={() => run(() => api.loginClaude(account.id))}
            >
              {tr('Sign in', '重新登录')}
            </Button>
          ) : (
            <>
              {!account.isActive && (
                <Button
                  disabled={busy || account.isLoggingIn}
                  onClick={() =>
                    run(() => api.switchClaude(account.id), tr('Account switched', '账号已切换'))
                  }
                >
                  <ArrowRightLeft size={14} />
                  {tr('Switch', '切换')}
                </Button>
              )}
              <Button
                disabled={busy || account.isLoggingIn}
                onClick={() => run(() => api.startClaude(account.id))}
              >
                <Play size={14} />
                {account.isActive ? tr('Start Claude Code', '启动 Claude Code') : tr('Start', '启动')}
              </Button>
            </>
          )}
          <ActionMenu label={tr('Account actions', '账号操作')}>
            <MenuItem disabled={busy} onSelect={() => rename(account)}>
              {tr('Rename', '重命名')}
            </MenuItem>
            <MenuItem
              disabled={busy || account.isLoggingIn}
              onSelect={() => {
                void run(() => api.loginClaude(account.id))
              }}
            >
              {tr('Sign in again', '重新登录')}
            </MenuItem>
            <MenuItem disabled={busy} danger onSelect={() => remove(account)}>
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
