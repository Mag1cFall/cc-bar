import { Call, type CancellablePromise } from '@wailsio/runtime'
import type {
  ClaudeAccount,
  CodexAccount,
  CodexPreview,
  ConversationDetail,
  ConversationPage,
  ConversationQuery,
  Cycles,
  Overview,
  ResetCredits,
  ResetResult,
  ReleaseUpdate,
  Settings,
  Snapshot,
  Timeline,
  UsageQuery,
} from './models'

// invoke 使用 Wails 完整包名调用真实 Go 服务
function invoke<T>(method: string, ...args: unknown[]): CancellablePromise<T> {
  const result = Call.ByName(
    `github.com/Mag1cFall/cc-bar/internal/app.Service.${method}`,
    ...args,
  ) as CancellablePromise<unknown>
  return result.then((value) => value as T)
}
export const api = {
  snapshot: () => invoke<Snapshot>('GetSnapshot'),
  saveSettings: (settings: Settings) => invoke<void>('SaveSettings', settings),
  refresh: () => invoke<void>('RefreshQuotas'),
  scan: (rebuild = false) => invoke<void>('ScanUsage', rebuild),
  overview: (query: UsageQuery) => invoke<Overview>('GetOverview', query),
  conversations: (query: ConversationQuery) => invoke<ConversationPage>('GetConversations', query),
  conversation: (id: string) => invoke<ConversationDetail>('GetConversation', id),
  cycles: () => invoke<Cycles>('GetCycles'),
  timeline: (query: { app: number | null; accountKey?: string; limitKind: number }) =>
    invoke<Timeline>('GetTimeline', query),
  saveClaude: () => invoke<void>('SaveClaudeAccount'),
  addClaude: (name: string) => invoke<ClaudeAccount>('AddClaudeAccount', name),
  loginClaude: (id: string) => invoke<void>('LoginClaudeAccount', id),
  switchClaude: (id: string) => invoke<void>('SwitchClaudeAccount', id),
  startClaude: (id: string) => invoke<void>('StartClaudeAccount', id),
  renameClaude: (id: string, name: string) => invoke<void>('RenameClaudeAccount', id, name),
  removeClaude: (id: string) => invoke<void>('RemoveClaudeAccount', id),
  refreshClaude: () => invoke<void>('RefreshClaudeAccounts'),
  previewCodex: (text: string) => invoke<CodexPreview[]>('PreviewCodex', text),
  importCodex: (text: string, visible: boolean) => invoke<void>('ImportCodex', text, visible),
  removeCodex: (id: string) => invoke<void>('RemoveCodex', id),
  saveCodex: (accounts: CodexAccount[]) => invoke<void>('SaveCodexAccounts', accounts),
  resetCredits: (id: string) => invoke<ResetCredits>('GetResetCredits', id),
  consumeCredit: (id: string, creditID: string) => invoke<ResetResult>('ConsumeResetCredit', id, creditID),
  commandKey: (key: string) => invoke<void>('SetCommandCodeKey', key),
  clearCommandKey: () => invoke<void>('ClearCommandCodeKey'),
  prices: () => invoke<void>('RefreshPrices'),
  diagnostics: () => invoke<string>('ExportDiagnostics'),
  dataDirectory: () => invoke<void>('OpenDataDirectory'),
  checkUpdates: () => invoke<ReleaseUpdate>('CheckForUpdates'),
  installUpdate: () => invoke<void>('InstallUpdate'),
  showMain: (page: string) => invoke<void>('ShowMain', page),
  presentMain: () => invoke<void>('PresentMain'),
  hudPosition: (x: number, y: number) => invoke<void>('SetHudPosition', x, y),
  resizeSurface: (width: number, height: number) => invoke<void>('ResizeSurface', width, height),
  hidePopover: () => invoke<void>('HidePopover'),
  quit: () => invoke<void>('Quit'),
}
