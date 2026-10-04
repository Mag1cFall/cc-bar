import { useState, type CSSProperties } from 'react'
import * as Collapsible from '@radix-ui/react-collapsible'
import { ArrowDownWideNarrow, ChevronDown } from 'lucide-react'
import { Bar, BarChart, CartesianGrid, Cell, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api } from '../bridge'
import { Card, Empty, Logo, Progress, QueryState, Section, Segments, Select } from '../components'
import { useApp } from '../context'
import { compact, delta, money, serviceColors, usageKeys, usageNames } from '../format'
import { useQuery } from '../hooks'
import type { Overview as OverviewData, UsageGroup, UsageQuery } from '../models'
import { TokenBreakdown } from './shared'

export default function Overview({ query }: { query: UsageQuery }) {
  const { revision, tr, english, snapshot } = useApp()
  const [metric, setMetric] = useState('cost')
  const result = useQuery(`${JSON.stringify(query)}:${revision}`, () => api.overview(query))
  const data = result.data
  const services = [...(data?.services ?? [])].sort((left, right) => (left.app ?? 0) - (right.app ?? 0))
  // 服务卡始终沿用侧栏的可见范围，筛选其他服务时显示短横线
  const visibleServices = usageNames
    .map((name, app) => ({ name, app }))
    .filter(
      ({ app }) =>
        snapshot.settings.usageVisibility[String(app)] &&
        (app !== 2 || snapshot.settings.providers['3']?.enabled),
    )
  const buckets = (data?.buckets ?? []).map((bucket) => {
    const record: Record<string, number | string> = { at: bucket.at }
    for (const group of bucket.services ?? [])
      record[`app${group.app ?? 0}`] = metric === 'cost' ? group.totals.cost : group.totals.tokens
    return record
  })
  return (
    <>
      <QueryState pending={result.pending} error={result.error} />
      {data && (
        <>
          <div className="kpi-grid" style={{ '--kpi-count': visibleServices.length + 2 } as CSSProperties}>
            <Kpi
              label={tr('Total tokens', '总 Tokens')}
              value={compact(data.totals.tokens, english)}
              change={query.compare ? delta(data.totals.tokens, data.previousTotals.tokens) : null}
            />
            <Kpi
              label={tr('Total spend', '总花费')}
              value={money(data.totals.cost)}
              change={query.compare ? delta(data.totals.cost, data.previousTotals.cost) : null}
            />
            {visibleServices.map(({ app, name }) => {
              const group = services.find((item) => item.app === app)
              const dimmed = query.service !== null && query.service !== app
              return (
                <Kpi
                  key={app}
                  label={name}
                  value={dimmed ? '—' : money(group?.totals.cost ?? 0)}
                  change={
                    query.compare && !dimmed
                      ? delta(group?.totals.cost ?? 0, group?.previousTotals.cost ?? 0)
                      : null
                  }
                  service={app}
                  dimmed={dimmed}
                />
              )
            })}
          </div>
          <Section title={tr('Token breakdown', 'Token 拆分')}>
            <Card className="padded">
              <div className="token-stacked">
                <span style={{ flexGrow: data.totals.input + data.totals.cacheWrite || 1 }} />
                <span style={{ flexGrow: data.totals.output || 1 }} />
                <span style={{ flexGrow: data.totals.cacheRead || 1 }} />
              </div>
              <TokenBreakdown totals={data.totals} variant="full" />
              {data.fast.totals.tokens > 0 && (
                <div className="fast-summary">
                  <div>
                    <span>Fast Tokens</span>
                    <strong>{compact(data.fast.totals.tokens, english)}</strong>
                  </div>
                  <div>
                    <span>{tr('Billing-equivalent tokens', '计费等效 Tokens')}</span>
                    <strong>
                      {data.fast.equivalentTokens === null
                        ? '—'
                        : compact(data.fast.equivalentTokens, english)}
                    </strong>
                  </div>
                  <div>
                    <span>{tr('Fast multiplier', 'Fast 倍率')}</span>
                    <strong>{data.fast.multiplierText}</strong>
                  </div>
                  <div>
                    <span>{tr('Fast estimated cost', 'Fast 估算费用')}</span>
                    <strong>{money(data.fast.totals.cost)}</strong>
                  </div>
                  <div>
                    <span>{tr('Fast share', 'Fast 占比')}</span>
                    <strong>
                      {data.totals.tokens
                        ? ((data.fast.totals.tokens / data.totals.tokens) * 100).toFixed(0)
                        : 0}
                      %
                    </strong>
                  </div>
                </div>
              )}
            </Card>
          </Section>
          <Section title={tr('By service', '按服务')}>
            <Card className="padded">
              <div className="service-breakdown">
                {services.map((group) => (
                  <div className="service-summary" key={group.id}>
                    <div className="summary-heading">
                      <Logo name={usageKeys[group.app ?? 0] ?? ''} size={22} />
                      <h3>{group.name}</h3>
                      <span>
                        {data.totals.tokens
                          ? ((group.totals.tokens / data.totals.tokens) * 100).toFixed(0)
                          : '0'}
                        %
                      </span>
                    </div>
                    <Progress
                      value={data.totals.tokens ? (group.totals.tokens / data.totals.tokens) * 100 : 0}
                      color={serviceColors[group.app ?? 0]}
                    />
                    <div className="summary-metric">
                      <strong>{compact(group.totals.tokens, english)} Tokens</strong>
                      <strong>{money(group.totals.cost)}</strong>
                    </div>
                    <TokenBreakdown totals={group.totals} />
                  </div>
                ))}
              </div>
              {services.length === 0 && <Empty title={tr('No usage in this range', '此范围内暂无用量')} />}
            </Card>
          </Section>
          <Section
            title={
              query.grain === 'month'
                ? tr('Monthly usage', '每月用量')
                : query.grain === 'week'
                  ? tr('Weekly usage', '每周用量')
                  : tr('Daily usage', '每日用量')
            }
            actions={
              <Segments
                label={tr('Chart metric', '图表指标')}
                value={metric}
                onChange={setMetric}
                options={[
                  { value: 'cost', label: tr('Cost', '费用') },
                  { value: 'tokens', label: 'Tokens' },
                ]}
              />
            }
          >
            <Card className="padded">
              <div className="chart-legend">
                {services.map((group) => (
                  <span key={group.id}>
                    <i style={{ background: serviceColors[group.app ?? 0] }} />
                    {group.name}
                  </span>
                ))}
              </div>
              {buckets.length ? (
                <div className="usage-chart">
                  <ResponsiveContainer width="100%" height="100%">
                    <BarChart data={buckets} margin={{ top: 10, right: 8, bottom: 6, left: 4 }}>
                      <CartesianGrid stroke="var(--separator)" vertical={false} />
                      <XAxis
                        dataKey="at"
                        tickFormatter={(value: string) =>
                          new Date(value).toLocaleDateString(english ? 'en' : 'zh', {
                            month: 'numeric',
                            day: query.grain === 'month' ? undefined : 'numeric',
                          })
                        }
                        tick={{ fill: 'var(--secondary)', fontSize: 11 }}
                        axisLine={false}
                        tickLine={false}
                        minTickGap={22}
                      />
                      <YAxis
                        width={58}
                        tickFormatter={(value: number) =>
                          metric === 'cost' ? `$${value.toFixed(0)}` : compact(value, english)
                        }
                        tick={{ fill: 'var(--secondary)', fontSize: 11 }}
                        axisLine={false}
                        tickLine={false}
                      />
                      <Tooltip
                        content={({ active, label }) => (
                          <BucketTooltip
                            active={active}
                            at={typeof label === 'string' ? label : undefined}
                            buckets={data.buckets ?? []}
                          />
                        )}
                        cursor={{ fill: 'var(--hover)' }}
                      />
                      {services.map((group) => (
                        <Bar
                          key={group.id}
                          name={usageNames[group.app ?? 0]}
                          dataKey={`app${group.app ?? 0}`}
                          stackId="usage"
                          fill={serviceColors[group.app ?? 0]}
                          maxBarSize={36}
                          isAnimationActive={false}
                        >
                          {(data.buckets ?? []).map((bucket) => (
                            <Cell
                              key={bucket.at}
                              fillOpacity={
                                new Date(bucket.at).valueOf() >= new Date(query.from).valueOf() &&
                                new Date(bucket.at).valueOf() < new Date(query.to).valueOf()
                                  ? 1
                                  : 0.4
                              }
                            />
                          ))}
                        </Bar>
                      ))}
                    </BarChart>
                  </ResponsiveContainer>
                </div>
              ) : (
                <Empty title={tr('No chart data', '暂无图表数据')} />
              )}
            </Card>
          </Section>
          <GroupTable title={tr('By model', '按模型')} groups={data.models ?? []} />
          <GroupTable title={tr('By provider', '按提供商')} groups={data.providers ?? []} expandable />
        </>
      )}
    </>
  )
}

function BucketTooltip({
  active,
  at,
  buckets,
}: {
  active?: boolean
  at?: string
  buckets: NonNullable<OverviewData['buckets']>
}) {
  const { english, tr } = useApp()
  const bucket = buckets.find((value) => value.at === at)
  if (!active || !bucket) return null
  return (
    <div className="card padded" style={{ width: 260, background: 'var(--surface)' }}>
      <p className="secondary">{new Date(bucket.at).toLocaleDateString(english ? 'en' : 'zh')}</p>
      {(bucket.services ?? []).map((group) => (
        <div className="summary-heading" key={group.id}>
          <span className="service-dot" style={{ background: serviceColors[group.app ?? 0] }} />
          <span>{group.name}</span>
          <strong>{money(group.totals.cost)}</strong>
        </div>
      ))}
      <div className="summary-metric">
        <span>{tr('Total', '合计')}</span>
        <strong>{money(bucket.totals.cost)}</strong>
      </div>
      <div className="summary-metric">
        <span>Tokens</span>
        <strong>{compact(bucket.totals.tokens, english)}</strong>
      </div>
      <TokenBreakdown totals={bucket.totals} />
    </div>
  )
}

function Kpi({
  label,
  value,
  change,
  service,
  dimmed = false,
}: {
  label: string
  value: string
  change: string | null
  service?: number
  dimmed?: boolean
}) {
  return (
    <Card className={`kpi ${dimmed ? 'dimmed' : ''}`}>
      <div className="kpi-label">
        {service !== undefined && (
          <span className="service-dot" style={{ background: serviceColors[service] }} />
        )}
        {label}
      </div>
      <strong className="kpi-value numeric">{value}</strong>
      {change && <span className="delta numeric">{change}</span>}
    </Card>
  )
}

function GroupTable({
  title,
  groups,
  expandable = false,
}: {
  title: string
  groups: UsageGroup[]
  expandable?: boolean
}) {
  const { tr, english } = useApp()
  const [sort, setSort] = useState('cost'),
    [expanded, setExpanded] = useState<string>()
  const ordered = [...groups].sort((left, right) =>
    sort === 'name'
      ? left.name.localeCompare(right.name)
      : sort === 'tokens'
        ? right.totals.tokens - left.totals.tokens
        : sort === 'requests'
          ? right.totals.requests - left.totals.requests
          : right.totals.cost - left.totals.cost,
  )
  return (
    <Section
      title={title}
      actions={
        <Select
          label={tr('Sort by', '排序方式')}
          icon={<ArrowDownWideNarrow size={14} />}
          value={sort}
          onChange={setSort}
          options={[
            { value: 'cost', label: tr('Cost', '费用') },
            { value: 'tokens', label: 'Tokens' },
            { value: 'requests', label: tr('Requests', '请求数') },
            { value: 'name', label: tr('Name', '名称') },
          ]}
        />
      }
    >
      <Card className="padded group-card">
        {ordered.length === 0 && <Empty title={tr('No data', '暂无数据')} />}
        {ordered.map((group) => (
          <Collapsible.Root
            key={group.id}
            className="group"
            open={expanded === group.id}
            onOpenChange={(open) => setExpanded(open ? group.id : undefined)}
          >
            <Collapsible.Trigger asChild>
              <button className="group-heading" type="button">
                <span className="group-name">
                  {group.app !== undefined && <Logo name={usageKeys[group.app] ?? ''} size={18} />}
                  <strong>{group.name}</strong>
                  {group.speed && <span className="badge">{group.speed}</span>}
                </span>
                <span className="group-values numeric">
                  <span>{compact(group.totals.tokens, english)} Tokens</span>{' '}
                  <strong>{money(group.totals.cost)}</strong>
                  <ChevronDown size={16} className="disclosure-chevron" />
                </span>
              </button>
            </Collapsible.Trigger>
            {!expandable && (
              <div className="group-details">
                <TokenBreakdown totals={group.totals} />
              </div>
            )}
            <Collapsible.Content className="disclosure-content">
              <div className="group-expanded">
                <TokenBreakdown totals={group.totals} variant={expandable ? 'full' : 'additional'} />
                {(group.models ?? []).map((model) => (
                  <div className="submodel" key={model.id}>
                    <div className="summary-heading">
                      <span>{model.name}</span>
                      {model.speed && <span className="badge">{model.speed}</span>}
                      <strong className="numeric">
                        {compact(model.totals.tokens, english)} · {money(model.totals.cost)}
                      </strong>
                    </div>
                    <TokenBreakdown totals={model.totals} />
                  </div>
                ))}
              </div>
            </Collapsible.Content>
          </Collapsible.Root>
        ))}
      </Card>
    </Section>
  )
}
