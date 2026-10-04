// Settings 对应后端持久化设置并保留数字枚举键
export interface Settings {
  providers: Record<string, { enabled: boolean; menuBar: boolean; floatingHud: boolean }>
  usageVisibility: Record<string, boolean>
  floatingEnabled: boolean
  didCompleteOnboarding: boolean
  launchAtLogin: boolean
  privacyMode: boolean
  showServiceStatus: boolean
  verboseLogging: boolean
  quotaIntervalMinutes: number
  usageIntervalMinutes: number
  menuBarWindow: string
  resetTimeDisplay: string
  language: string
  theme: 'system' | 'light' | 'dark'
  commandCodeCredentialPreference: string
  hudLeft?: number
  hudTop?: number
}
export interface Credential {
  email?: string
  accountUuid?: string
  accountId?: string
  login?: string
  subscriptionType?: string
  source: string
}
export interface QuotaLimit {
  id: string
  kind: number
  displayName?: string
  window: { usedPercent: number; resetsAt?: string; windowSeconds?: number }
  isActive?: boolean
}
export interface QuotaSnapshot {
  app: number
  primaryLimit?: QuotaLimit
  secondaryLimit?: QuotaLimit
  auxiliaryLimits: QuotaLimit[] | null
  modelLimits: QuotaLimit[] | null
  geminiWindow?: QuotaLimit['window']
  geminiWeekly?: QuotaLimit['window']
  isUnlimited?: boolean
  planType?: string
  fetchedAt: string
}
export interface Provider {
  app: number
  name: string
  account?: Credential
  snapshot?: QuotaSnapshot
  error?: string
  errorKind?: string
  source?: string
  lastSuccessAt?: string
  refreshing: boolean
}
export interface ClaudeAccount {
  id: string
  name: string
  email?: string
  plan?: string
  isActive: boolean
  desktopSaved?: boolean
  desktopActive: boolean
  historyShared: boolean
  needsLogin: boolean
  isLoggingIn: boolean
  error?: string
  snapshot?: QuotaSnapshot
}
export interface ClaudeLogin {
  id: string
  accountId?: string
  stage: string
  url?: string
  error?: string
  mode: string
}
export interface CodexAccount {
  id: string
  displayName: string
  alias: string
  email?: string
  planType?: string
  visibleInPopover: boolean
  snapshot?: QuotaSnapshot
  error?: string
  accountId?: string
}
export interface Snapshot {
  version: string
  settings: Settings
  providers: Provider[]
  claudeAccounts: ClaudeAccount[]
  claudeLogin?: ClaudeLogin
  importedCodexAccounts: CodexAccount[]
  serviceStatuses: Record<string, string>
  scan: {
    revision: number
    isScanning: boolean
    error: string
    filesCompleted?: number
    filesTotal?: number
    lastScanAt?: string
  }
  locale: string
  localServices: Record<string, { detected: boolean; source: string }>
}
export interface UsageTotals {
  input: number
  output: number
  cacheRead: number
  cacheWrite: number
  cacheWrite1h: number
  requests: number
  cost: number
  tokens: number
  cacheHitRate: number
}
export interface UsageGroup {
  id: string
  name: string
  app?: number
  model?: string
  provider?: string
  speed?: string
  totals: UsageTotals
  previousTotals: UsageTotals
  models: UsageGroup[] | null
}
export interface UsageQuery {
  service: number | null
  grain: string
  from: string
  to: string
  compare: boolean
}
export interface Overview {
  totals: UsageTotals
  previousTotals: UsageTotals
  services: UsageGroup[] | null
  models: UsageGroup[] | null
  providers: UsageGroup[] | null
  buckets: { at: string; totals: UsageTotals; services: UsageGroup[] | null }[] | null
  fast: {
    totals: UsageTotals
    standardTotals: UsageTotals
    equivalentTokens: number | null
    unknownMultiplier: boolean
    multiplierText: string
  }
}
export interface ConversationQuery {
  app: number | null
  from: string
  to: string
  search: string
  project: string | null
  sort: string
  offset: number
  limit: number
}
export interface Conversation {
  id: string
  title: string
  app: number
  project: string
  projectStatus: string
  cwd: string
  branch: string
  subtasks: boolean
  cacheWriteAvail: boolean
  startedAt: string
  endedAt: string
  models: string[] | null
  speed: string
  totals: UsageTotals
}
export interface ConversationPage {
  items: Conversation[] | null
  total: number
  projects: { id: string; name: string; status: string; count: number; lastAt: string }[] | null
}
export interface ConversationDetail {
  conversation: Conversation
  events:
    | {
        id: string
        app: number
        conversation: string
        model: string
        speed: string
        time: string
        input: number
        output: number
        cacheRead: number
        cacheWrite: number
        cacheWrite1h: number
        requests: number
        cost: number | null
      }[]
    | null
  models: UsageGroup[] | null
  costs: { input: number; output: number; cacheRead: number; cacheWrite: number }
}
export interface Cycle {
  id: string
  accountKey: string
  app: number
  limitId: string
  limitKind: number
  startAt: string
  endAt?: string
  scheduledEndAt?: string
  latestUsedPercent: number
  allowanceSegments:
    | {
        id: string
        startAt: string
        endAt?: string
        baselineUsedPercent: number
        latestUsedPercent: number
        maximumUsedPercent: number
        firstSampleAt: string
        lastSampleAt: string
        startReason: number
      }[]
    | null
  source: string
  boundaryQuality: number
  extraResetCount: number
  totalObservedUsedPercent: number
  reportedUsedPercent: number
  localTotals: UsageTotals
  estimatedTotalCost?: number
  remainingLocalCost?: number
  estimatedTotalTokens?: number
  remainingLocalTokens?: number
  forecastConfidence?: 'early' | 'rough' | 'reference' | 'reliable'
  isCurrent: boolean
  isActiveAccount: boolean
}
export interface Cycles {
  trackingStartedAt: string
  records: Cycle[] | null
}
export interface ReleaseUpdate {
  currentVersion: string
  latestVersion?: string
  status: 'unpublished' | 'current' | 'available'
  releaseUrl?: string
  notes?: string
  size?: number
  installError?: string
}
export interface Timeline {
  accounts:
    | {
        key: string
        name: string
        app: number
        periods:
          | {
              id: string
              kind: number
              start: string
              end: string
              entries:
                | {
                    id: string
                    isChange: boolean
                    sampledAt: string
                    remainingPercent: number
                    deltaPercent?: number
                    resetsAt?: string
                    windowIndex: number
                  }[]
                | null
              totalDelta: number
            }[]
          | null
      }[]
    | null
}
export interface ResetCredit {
  id: string
  title: string
  status: string
  grantedAt?: string
  expiresAt?: string
}
export interface ResetCredits {
  available: number
  credits: ResetCredit[] | null
}
export interface ResetResult {
  code: string
  windowsReset: number
  credits?: ResetCredits
}
export interface CodexPreview {
  email?: string
  plan?: string
  accountId?: string
  userId?: string
  personalToken: boolean
}
