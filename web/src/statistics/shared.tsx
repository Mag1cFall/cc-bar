import { useApp } from '../context'
import { compact, money } from '../format'
import type { UsageTotals } from '../models'

export function TokenBreakdown({ totals, extended = false }: { totals: UsageTotals; extended?: boolean }) {
  const { tr, english } = useApp()
  const values = [
    [tr('Input', '输入'), compact(totals.input, english)],
    [tr('Output', '输出'), compact(totals.output, english)],
    [tr('Cache read', '缓存读取'), compact(totals.cacheRead, english)],
    [tr('Hit rate', '命中率'), `${totals.cacheHitRate.toFixed(0)}%`],
  ]
  if (extended)
    values.push(
      [tr('Cache write', '缓存写入'), compact(totals.cacheWrite, english)],
      [tr('1-hour cache write', '1 小时缓存写入'), compact(totals.cacheWrite1h, english)],
      [tr('Requests', '请求数'), totals.requests.toLocaleString()],
      [tr('Cost', '费用'), money(totals.cost)],
    )
  return (
    <div className={`token-grid ${extended ? 'extended' : ''}`}>
      {values.map(([label, value]) => (
        <div key={label}>
          <span>{label}</span>
          <strong className="numeric">{value}</strong>
        </div>
      ))}
    </div>
  )
}
