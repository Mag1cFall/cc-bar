import { useState } from 'react'
import { RefreshCw } from 'lucide-react'
import { api } from './bridge'
import { useApp } from './context'
import { Button, QueryState, Segments } from './components'
import { dateBounds, localDate, usageNames } from './format'
import type { UsageQuery } from './models'
import Overview from './statistics/Overview'
import Conversations from './statistics/Conversations'
import Cycles from './statistics/Cycles'
import Timeline from './statistics/Timeline'

// Statistics 保持服务筛选与日周月日期范围独立于各统计页面
export default function Statistics({ page, service }: { page: string; service: number | null }) {
  const { tr, busy, run, snapshot } = useApp()
  const [grain, setGrain] = useState('day')
  const [range, setRange] = useState('today')
  const [customFrom, setCustomFrom] = useState(() => localDate(new Date(Date.now() - 7 * 86_400_000)))
  const [customTo, setCustomTo] = useState(() => localDate(new Date()))
  const query: UsageQuery = {
    service,
    grain,
    ...dateBounds(range, customFrom, customTo),
    compare: range !== 'all' && range !== 'custom',
  }
  const title =
    page === 'conversations'
      ? tr('Conversations', '对话')
      : page === 'cycles'
        ? tr('Quota cycles', '额度周期')
        : page === 'timeline'
          ? tr('Quota timeline', '额度时间线')
          : service !== null
            ? (usageNames[service] ?? '')
            : tr('Overview', '概览')
  const rangesFor = (value: string) => [
    ...(value === 'week'
      ? [
          ['week', 'This week', '本周'],
          ['last-week', 'Last week', '上周'],
          ['4w', '4 weeks', '4 周'],
          ['12w', '12 weeks', '12 周'],
        ]
      : value === 'month'
        ? [
            ['month', 'This month', '本月'],
            ['last-month', 'Last month', '上月'],
            ['6m', '6 months', '6 个月'],
            ['year', 'This year', '本年'],
          ]
        : [
            ['today', 'Today', '今天'],
            ['yesterday', 'Yesterday', '昨天'],
            ['7d', '7 days', '7 天'],
            ['30d', '30 days', '30 天'],
            ['week', 'This week', '本周'],
            ['month', 'This month', '本月'],
            ['year', 'This year', '本年'],
          ]),
    ['all', 'All', '全部'],
    ['custom', 'Custom', '自定义'],
  ]
  const ranges = rangesFor(grain)
  const chooseGrain = (value: string) => {
    setGrain(value)
    const choices = rangesFor(value)
    if (!choices.some(([key]) => key === range)) setRange(choices[0]?.[0] ?? 'today')
  }
  const datesVisible = page !== 'timeline' && page !== 'cycles'
  return (
    <div className={`statistics-content statistics-${page}`}>
      <header className="page-heading statistics-heading">
        <div>
          <h1>{title}</h1>
          {service !== null && page !== 'overview' && <p>{usageNames[service]}</p>}
        </div>
        {datesVisible && (
          <div className="date-toolbar">
            <Segments
              value={grain}
              onChange={chooseGrain}
              label={tr('Chart grain', '图表粒度')}
              options={[
                { value: 'day', label: tr('Day', '日') },
                { value: 'week', label: tr('Week', '周') },
                { value: 'month', label: tr('Month', '月') },
              ]}
            />
            <Segments
              value={range}
              onChange={setRange}
              label={tr('Date range', '日期范围')}
              options={ranges.map(([value, english, chinese]) => ({
                value: value ?? '',
                label: tr(english ?? '', chinese ?? ''),
              }))}
            />
          </div>
        )}
        <div className="actions">
          <QueryState pending={snapshot.scan.isScanning} error={snapshot.scan.error} />
          <Button
            tone="ghost"
            className="icon-button"
            disabled={busy || snapshot.scan.isScanning}
            title={tr('Refresh local usage', '刷新本地用量')}
            aria-label={tr('Refresh local usage', '刷新本地用量')}
            onClick={() => run(() => api.scan())}
          >
            <RefreshCw size={17} />
          </Button>
        </div>
      </header>
      {datesVisible && range === 'custom' && (
        <div className="custom-dates">
          <label>
            <span className="sr-only">{tr('Start date', '开始日期')}</span>
            <input
              type="date"
              value={customFrom}
              max={customTo}
              onChange={(event) => {
                if (event.target.value) setCustomFrom(event.target.value)
              }}
            />
          </label>
          <span>~</span>
          <label>
            <span className="sr-only">{tr('End date', '结束日期')}</span>
            <input
              type="date"
              value={customTo}
              min={customFrom}
              onChange={(event) => {
                if (event.target.value) setCustomTo(event.target.value)
              }}
            />
          </label>
        </div>
      )}
      {page === 'conversations' ? (
        <Conversations query={query} />
      ) : page === 'cycles' ? (
        <Cycles service={service} />
      ) : page === 'timeline' ? (
        <Timeline service={service} />
      ) : (
        <Overview query={query} />
      )}
    </div>
  )
}
