export const quotaNames = ['Codex', 'Claude Code', 'Antigravity', 'Cursor', 'Command Code']
export const usageNames = ['Codex', 'Claude Code', 'Cursor', 'Pi', 'OpenCode', 'DSH', 'Oh My Pi']
export const quotaKeys = ['codex', 'claude', 'antigravity', 'cursor', 'commandcode']
export const usageKeys = ['codex', 'claude', 'cursor', 'pi', 'opencode', 'dsh', 'omp']
export const serviceColors = ['#74757d', '#d97757', '#303034', '#4b7ba9', '#168179', '#4176e6', '#9b4dff']

// compact 根据界面语言缩写令牌数量
export function compact(value: number, english: boolean): string {
  if (!english && value >= 100_000_000) return `${(value / 100_000_000).toFixed(2)}亿`
  if (!english && value >= 10_000) return `${(value / 10_000).toFixed(1)}万`
  return new Intl.NumberFormat(english ? 'en' : 'zh', {
    notation: 'compact',
    maximumFractionDigits: 1,
  }).format(value)
}
export function money(value: number): string {
  return `$${value.toLocaleString('en', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`
}
export function dateTime(value: string | undefined, english: boolean, short = false): string {
  if (!value || !Number.isFinite(new Date(value).valueOf())) return '—'
  return new Date(value).toLocaleString(
    english ? 'en-US' : 'zh-CN',
    short
      ? { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }
      : {
          year: 'numeric',
          month: '2-digit',
          day: '2-digit',
          hour: '2-digit',
          minute: '2-digit',
          hour12: false,
        },
  )
}
export function remaining(used: number | undefined): number | undefined {
  return used === undefined ? undefined : Math.max(0, Math.min(100, 100 - used))
}
export function quotaTone(value: number | undefined): string {
  return value === 0 ? 'empty' : value !== undefined && value < 20 ? 'low' : 'normal'
}
export function relativeTime(value: string | undefined, english: boolean, now = Date.now()): string {
  if (!value) return '—'
  const minutes = Math.max(0, Math.ceil((new Date(value).valueOf() - now) / 60_000))
  if (minutes === 0) return english ? 'Resetting' : '即将重置'
  const hours = Math.floor(minutes / 60),
    days = Math.floor(hours / 24)
  return days > 0
    ? `${days}${english ? 'd' : '天'} ${hours % 24}${english ? 'h' : '小时'}`
    : hours > 0
      ? `${hours}${english ? 'h' : '小时'} ${minutes % 60}${english ? 'm' : '分'}`
      : `${minutes}${english ? 'm' : '分钟'}`
}
export function delta(value: number, previous: number): string | null {
  if (previous <= 0) return null
  const change = (value / previous - 1) * 100
  return `${change > 0 ? '+' : ''}${change.toFixed(0)}%`
}
export function localDate(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

// dateBounds 与原版保持本地自然日周月边界及闭开区间
export function dateBounds(
  range: string,
  customFrom: string,
  customTo: string,
  now = new Date(),
): { from: string; to: string } {
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const week = new Date(today)
  week.setDate(today.getDate() - ((today.getDay() + 6) % 7))
  const month = new Date(today.getFullYear(), today.getMonth(), 1)
  let start = new Date(today),
    end = new Date(today)
  end.setDate(end.getDate() + 1)
  switch (range) {
    case 'yesterday':
      start.setDate(start.getDate() - 1)
      end = today
      break
    case 'week':
      start = week
      break
    case 'last-week':
      start = new Date(week)
      start.setDate(start.getDate() - 7)
      end = week
      break
    case 'month':
      start = month
      break
    case 'last-month':
      start = new Date(month.getFullYear(), month.getMonth() - 1, 1)
      end = month
      break
    case 'year':
      start = new Date(today.getFullYear(), 0, 1)
      break
    case '7d':
      start.setDate(start.getDate() - 6)
      break
    case '30d':
      start.setDate(start.getDate() - 29)
      break
    case '4w':
      start = new Date(week)
      start.setDate(start.getDate() - 21)
      break
    case '12w':
      start = new Date(week)
      start.setDate(start.getDate() - 77)
      break
    case '6m':
      start = new Date(month.getFullYear(), month.getMonth() - 5, 1)
      break
    case 'all':
      start = new Date(0)
      break
    case 'custom':
      start = new Date(`${customFrom}T00:00:00`)
      end = new Date(`${customTo}T00:00:00`)
      end.setDate(end.getDate() + 1)
      break
  }
  return { from: start.toISOString(), to: end.toISOString() }
}
