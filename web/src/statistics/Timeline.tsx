import { useState } from 'react'
import { CartesianGrid, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api } from '../bridge'
import { Card, Empty, QueryState, Section, Segments, Select } from '../components'
import { useApp } from '../context'
import { dateTime, usageNames } from '../format'
import { useQuery } from '../hooks'
import type { Timeline as TimelineModel } from '../models'

export default function Timeline({ service }: { service: number | null }) {
  const { tr, revision, snapshot } = useApp()
  const [limitKind, setLimitKind] = useState('0')
  const supportsQuota = service === null || service <= 2
  const request = {
    app: service === null || !supportsQuota ? null : service === 2 ? 3 : service,
    limitKind: Number(limitKind),
  }
  const quotaRevision = [
    ...snapshot.providers.map((provider) => provider.snapshot?.fetchedAt),
    ...snapshot.claudeAccounts.map((account) => account.snapshot?.fetchedAt),
    ...snapshot.importedCodexAccounts.map((account) => account.snapshot?.fetchedAt),
  ].join()
  const result = useQuery(
    `${JSON.stringify(request)}:${revision}:${quotaRevision}`,
    () => api.timeline(request),
    supportsQuota,
  )
  if (!supportsQuota)
    return (
      <Card>
        <Empty
          title={tr('Local usage only', '本地用量统计')}
          description={tr('This service has no account quota timeline.', '该服务未提供账号额度时间线')}
        />
      </Card>
    )
  return (
    <>
      <div className="timeline-toolbar">
        <p className="secondary">
          {tr('Quota samples and usage changes by account.', '按账号查看额度采样与用量变化')}
        </p>
        <Segments
          value={limitKind}
          onChange={setLimitKind}
          label={tr('Quota window', '额度窗口')}
          options={[
            { value: '0', label: tr('5-hour', '5 小时') },
            { value: '1', label: tr('Weekly', '周额度') },
          ]}
        />
      </div>
      <QueryState pending={result.pending} error={result.error} />
      {result.data && (
        <>
          {(result.data.accounts ?? []).map((account) => (
            <AccountTimeline
              key={`${account.key}:${limitKind}`}
              account={account}
              limitKind={Number(limitKind)}
            />
          ))}
          {!(result.data.accounts ?? []).length && (
            <Card>
              <Empty
                title={tr('No quota timeline yet', '暂无额度时间线')}
                description={tr('Refresh a connected service to begin tracking.', '刷新已连接服务以开始追踪')}
              />
            </Card>
          )}
        </>
      )}
    </>
  )
}

function AccountTimeline({
  account,
  limitKind,
}: {
  account: NonNullable<TimelineModel['accounts']>[number]
  limitKind: number
}) {
  const { tr, english, snapshot } = useApp()
  const periods = account.periods ?? []
  const initialPeriod = periods.find((value) => value.entries?.length) ?? periods[0]
  const [selected, setSelected] = useState(initialPeriod?.id ?? '')
  const period = periods.find((value) => value.id === selected) ?? initialPeriod
  const entries = period?.entries ?? []
  const data = entries.map((entry) => ({ ...entry, at: new Date(entry.sampledAt).valueOf() }))
  const names = ['Codex', 'Claude Code', 'Antigravity', 'Cursor', 'Command Code']
  const serviceName = names[account.app] ?? usageNames[account.app] ?? ''
  const accountName = snapshot.settings.privacyMode ? tr('Account', '账号') : account.name
  return (
    <Section
      title={accountName && accountName !== serviceName ? `${serviceName} · ${accountName}` : serviceName}
    >
      <Card className="padded">
        <div className="timeline-periods">
          <Select
            label={tr('Timeline period', '时间线周期')}
            value={period?.id ?? ''}
            onChange={setSelected}
            options={periods.map((value) => ({
              value: value.id,
              label:
                value.kind === 0
                  ? new Date(value.start).toDateString() === new Date().toDateString()
                    ? tr('Today', '今天')
                    : new Date(value.start).toLocaleDateString(english ? 'en' : 'zh', {
                        month: 'numeric',
                        day: 'numeric',
                      })
                  : value.kind === 1
                    ? tr('Current cycle', '当前周期')
                    : tr('Previous cycle', '上一周期'),
            }))}
          />
          <span className="numeric">
            {tr('Observed usage', '观测用量')} {period?.totalDelta.toFixed(1) ?? '0'}%
          </span>
        </div>
        {period && (
          <p className="secondary timeline-boundary">
            {dateTime(period.start, english)} ~ {dateTime(period.end, english)}
          </p>
        )}
        {entries.length ? (
          <div className="timeline-columns">
            <div className="timeline-chart">
              <ResponsiveContainer width="100%" height="100%">
                <LineChart data={data} margin={{ left: -10, right: 12, top: 14, bottom: 8 }}>
                  <CartesianGrid vertical={false} stroke="var(--separator)" />
                  <XAxis
                    type="number"
                    domain={
                      data.length === 1
                        ? [Number(data[0]?.at) - 30 * 60_000, Number(data[0]?.at) + 30 * 60_000]
                        : ['dataMin', 'dataMax']
                    }
                    dataKey="at"
                    tickFormatter={(value: number) =>
                      new Date(value).toLocaleString(
                        english ? 'en' : 'zh',
                        limitKind === 0
                          ? { hour: '2-digit', minute: '2-digit', hour12: false }
                          : { month: 'numeric', day: 'numeric' },
                      )
                    }
                    axisLine={false}
                    tickLine={false}
                    tick={{ fontSize: 11, fill: 'var(--secondary)' }}
                  />
                  <YAxis
                    domain={[0, 100]}
                    tickFormatter={(value: number) => `${value}%`}
                    axisLine={false}
                    tickLine={false}
                    tick={{ fontSize: 11, fill: 'var(--secondary)' }}
                  />
                  <Tooltip
                    contentStyle={{
                      background: 'var(--surface)',
                      border: '1px solid var(--separator)',
                      borderRadius: 8,
                      fontSize: 12,
                    }}
                    labelFormatter={(value) => dateTime(new Date(Number(value)).toISOString(), english)}
                    formatter={(value) => `${Number(value).toFixed(1)}%`}
                  />
                  <Line
                    name={tr('Remaining', '剩余额度')}
                    dataKey="remainingPercent"
                    type="stepAfter"
                    stroke="var(--chart-line)"
                    strokeWidth={2}
                    dot={data.length < 15 ? { r: 3, strokeWidth: 0 } : false}
                    activeDot={{ r: 4 }}
                    isAnimationActive={false}
                  />
                </LineChart>
              </ResponsiveContainer>
            </div>
            <div className="timeline-table table-card">
              <table>
                <thead>
                  <tr>
                    <th>{tr('Time', '时间')}</th>
                    <th>{tr('Remaining', '剩余')}</th>
                    <th>{tr('Change', '变化')}</th>
                    <th>{tr('Window', '窗口')}</th>
                  </tr>
                </thead>
                <tbody>
                  {[...entries].reverse().map((entry) => (
                    <tr key={entry.id}>
                      <td>{dateTime(entry.sampledAt, english, true)}</td>
                      <td>{entry.remainingPercent.toFixed(1)}%</td>
                      <td>
                        {entry.deltaPercent === undefined
                          ? '—'
                          : `${entry.deltaPercent > 0 ? '+' : ''}${entry.deltaPercent.toFixed(1)}%`}
                      </td>
                      <td>{entry.windowIndex + 1}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        ) : (
          <Empty title={tr('No samples in this period', '此周期暂无采样')} />
        )}
      </Card>
    </Section>
  )
}
