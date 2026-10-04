import { api } from '../bridge'
import * as Collapsible from '@radix-ui/react-collapsible'
import { ChevronDown } from 'lucide-react'
import { Card, Empty, Logo, QueryState, Section } from '../components'
import { useApp } from '../context'
import {
  compact,
  dateTime,
  money,
  quotaTone,
  relativeTime,
  remaining,
  usageKeys,
  usageNames,
} from '../format'
import { useQuery } from '../hooks'
import type { Cycle } from '../models'
import { TokenBreakdown } from './shared'

export default function Cycles({ service }: { service: number | null }) {
  const { tr, english, revision, snapshot } = useApp()
  const quotaRevision = [
    ...snapshot.providers.map((provider) => provider.snapshot?.fetchedAt),
    ...snapshot.claudeAccounts.map((account) => account.snapshot?.fetchedAt),
  ].join()
  const result = useQuery(`${revision}:${quotaRevision}`, api.cycles)
  const records = (result.data?.records ?? []).filter((record) => service === null || record.app === service)
  const current = records.filter((record) => record.isCurrent && record.isActiveAccount)
  const apps = [0, 1].filter((app) => service === null || service === app)
  const historical = records
    .filter((record) => !record.isCurrent || !record.isActiveAccount)
    .sort((left, right) => right.startAt.localeCompare(left.startAt))
  return (
    <>
      <QueryState pending={result.pending} error={result.error} />
      {result.data && (
        <>
          <p className="page-note">
            {tr('Tracking since', '开始追踪于')} {dateTime(result.data.trackingStartedAt, english)}
          </p>
          <Section title={tr('Current cycles', '当前周期')}>
            <div className="cycle-grid">
              {apps.flatMap((app) =>
                [0, 1].map((kind) => (
                  <CycleCard
                    key={`${app}:${kind}`}
                    app={app}
                    kind={kind}
                    record={current.find((record) => record.app === app && record.limitKind === kind)}
                  />
                )),
              )}
            </div>
            {!apps.length && (
              <Card>
                <Empty
                  title={tr('No current quota cycles', '暂无当前额度周期')}
                  description={tr(
                    'Cycle tracking starts after a successful quota refresh.',
                    '额度刷新成功后开始追踪周期',
                  )}
                />
              </Card>
            )}
          </Section>
          <Section title={tr('Cycle history', '周期历史')}>
            <Card className="table-card">
              {historical.length ? (
                <table>
                  <thead>
                    <tr>
                      <th>{tr('Service', '服务')}</th>
                      <th>{tr('Period', '周期')}</th>
                      <th>{tr('Used', '已使用')}</th>
                      <th>Tokens</th>
                      <th>{tr('Local cost', '本地费用')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {historical.map((record) => (
                      <tr key={record.id}>
                        <td>
                          {usageNames[record.app]}
                          <span className="table-caption">
                            {record.limitKind === 0 ? tr('5-hour', '5 小时') : tr('Weekly', '周额度')}
                          </span>
                        </td>
                        <td>
                          {dateTime(record.startAt, english, true)}
                          <span className="table-caption">
                            {dateTime(record.endAt ?? record.scheduledEndAt, english, true)}
                          </span>
                        </td>
                        <td>
                          {record.totalObservedUsedPercent.toFixed(1)}%
                          {record.extraResetCount > 0 && (
                            <span className="table-caption">
                              {record.extraResetCount} {tr('extra resets', '次额外重置')}
                            </span>
                          )}
                        </td>
                        <td>{compact(record.localTotals.tokens, english)}</td>
                        <td>{money(record.localTotals.cost)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              ) : (
                <Empty title={tr('No completed cycles', '暂无已完成周期')} />
              )}
            </Card>
          </Section>
        </>
      )}
    </>
  )
}

function CycleCard({ record, app, kind }: { record?: Cycle; app: number; kind: number }) {
  const { tr, english, snapshot } = useApp()
  const provider = snapshot.providers.find((value) => value.app === app)
  const projected = record
  const confidence = projected?.forecastConfidence
  const caption =
    confidence === 'early'
      ? tr('Early estimate', '早期估算')
      : confidence === 'rough'
        ? tr('Rough estimate', '粗略估算')
        : confidence === 'reference'
          ? tr('Reference', '参考')
          : confidence === 'reliable'
            ? tr('More reliable', '较可靠')
            : ''
  const percent = Math.min(100, Math.max(0, record?.latestUsedPercent ?? 0))
  const tone = quotaTone(remaining(record?.latestUsedPercent))
  const resetsAt = record?.scheduledEndAt ?? record?.endAt
  return (
    <Card className="padded cycle-card">
      <div className="cycle-heading">
        <Logo name={usageKeys[app] ?? ''} size={14} />
        <span>
          {usageNames[app]} · {kind === 0 ? tr('5-hour', '5 小时') : tr('Weekly', '周')}
        </span>
        <span className="cycle-confidence">{caption}</span>
      </div>
      {record ? (
        <>
          <div className="cycle-projection numeric">
            {projected?.estimatedTotalTokens === undefined
              ? '—'
              : compact(projected.estimatedTotalTokens, english)}
            {' · '}
            {record.estimatedTotalCost === undefined
              ? '—'
              : `$${Math.round(record.estimatedTotalCost).toLocaleString('en')}`}
          </div>
          <p className="cycle-used numeric">
            {tr('Used', '已用')} {compact(record.localTotals.tokens, english)} ·{' '}
            {money(record.localTotals.cost)}
            <strong className={tone}>{percent.toFixed(1)}%</strong>
          </p>
          <div className={`progress ${tone}`} aria-label={tr('Quota used', '额度已用')}>
            <div style={{ width: `${percent}%` }} />
          </div>
          <p className="cycle-reset" title={dateTime(resetsAt, english)}>
            {snapshot.settings.resetTimeDisplay === 'absolute'
              ? dateTime(resetsAt, english)
              : `${tr('Resets in ', '重置剩余 ')}${relativeTime(resetsAt, english)}`}
          </p>
          <Collapsible.Root className="cycle-details">
            <Collapsible.Trigger className="cycle-details-trigger">
              {tr('Usage details', '用量详情')}
              <ChevronDown size={15} className="disclosure-chevron" />
            </Collapsible.Trigger>
            <Collapsible.Content className="disclosure-content">
              <div className="cycle-details-body">
                <TokenBreakdown totals={record.localTotals} />
                <p className="cycle-remaining">
                  {tr('Estimated remaining', '预计剩余费用')} ·{' '}
                  {record.remainingLocalCost === undefined ? '—' : money(record.remainingLocalCost)}
                </p>
                {record.extraResetCount > 0 && (
                  <p>
                    {record.extraResetCount} {tr('extra resets', '次额外重置')} ·{' '}
                    {record.allowanceSegments?.length ?? 0} {tr('allowance segments', '个额度分段')}
                  </p>
                )}
              </div>
            </Collapsible.Content>
          </Collapsible.Root>
        </>
      ) : (
        <div className="cycle-empty">
          <h3>
            {provider?.account
              ? tr('Waiting for the current cycle', '等待当前周期')
              : tr('Account not detected', '未检测到账号')}
          </h3>
          <p>{tr('Refresh quota to start tracking this cycle.', '刷新额度后开始追踪此周期')}</p>
        </div>
      )}
    </Card>
  )
}
