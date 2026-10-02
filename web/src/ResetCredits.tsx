import { useState } from 'react'
import { api } from './bridge'
import { useApp } from './context'
import { Button, Card, Dialog, Empty, QueryState } from './components'
import { dateTime } from './format'
import { useQuery } from './hooks'
import type { ResetCredits as Credits } from './models'

export default function ResetCredits({ id, name, close }: { id: string; name: string; close: () => void }) {
  const { tr, english, run, busy } = useApp()
  const [selected, setSelected] = useState<string>()
  const [openedAt] = useState(Date.now)
  const [refresh, setRefresh] = useState(0)
  const [updated, setUpdated] = useState<Credits>()
  const result = useQuery(`credits:${id}:${openedAt}:${refresh}`, () => api.resetCredits(id))
  const credits = updated ?? result.data
  const consume = async () => {
    if (!selected) return
    if (
      await run(
        async () => {
          const outcome = await api.consumeCredit(id, selected)
          if (outcome.credits) setUpdated(outcome.credits)
          else setRefresh((value) => value + 1)
          if (!['reset', 'alreadyRedeemed', 'already_redeemed'].includes(outcome.code)) {
            throw new Error(
              outcome.code === 'noCredit'
                ? tr('No reset credits available', '暂无可用重置额度')
                : outcome.code === 'nothingToReset' || outcome.code === 'nothing_to_reset'
                  ? tr('This account does not need a limit reset', '当前账号无需重置额度')
                  : outcome.code || tr('Reset request returned no result', '重置请求未返回结果'),
            )
          }
        },
        tr('Usage limits reset', '使用限额已重置'),
      )
    )
      setSelected(undefined)
  }
  return (
    <>
      <Dialog
        open
        onOpenChange={(open) => {
          if (!open) close()
        }}
        title={tr('Reset credits', '使用限额重置')}
        description={name}
        footer={<Button onClick={close}>{tr('Done', '完成')}</Button>}
      >
        <QueryState pending={result.pending} error={result.error} />
        {credits && (
          <>
            <div className="credit-heading">
              <span>{tr('Total available', '总可用额度')}</span>
              <span className="badge live">
                {credits.available} {tr('available', '次可用')}
              </span>
            </div>
            {(credits.credits ?? []).map((credit) => (
              <Card key={credit.id} className="credit-row">
                <div>
                  <h3>
                    {credit.title === 'full_reset' || credit.title === 'Full reset'
                      ? tr('Full reset (weekly + 5-hour)', '完全重置（每周 + 5 小时）')
                      : credit.title}
                  </h3>
                  <p>
                    {credit.expiresAt
                      ? `${tr('Expires', '到期时间')} ${dateTime(credit.expiresAt, english)}`
                      : credit.status}
                  </p>
                </div>
                <Button
                  disabled={
                    busy ||
                    !['active', 'available', ''].includes(credit.status.toLowerCase()) ||
                    (!!credit.expiresAt && new Date(credit.expiresAt).valueOf() <= openedAt)
                  }
                  onClick={() => setSelected(credit.id)}
                >
                  {tr('Use', '使用')}
                </Button>
              </Card>
            ))}
            {(credits.credits ?? []).length === 0 && (
              <Empty title={tr('No reset credits available', '暂无可用重置记录')} />
            )}
          </>
        )}
      </Dialog>
      <Dialog
        open={!!selected}
        onOpenChange={(open) => {
          if (!open) setSelected(undefined)
        }}
        title={tr('Use reset credit', '使用重置额度')}
        description={tr('One reset credit will be redeemed for this account.', '将为此账号消耗一次重置额度')}
        footer={
          <>
            <Button onClick={() => setSelected(undefined)}>{tr('Cancel', '取消')}</Button>
            <Button tone="primary" disabled={busy} onClick={consume}>
              {tr('Confirm reset', '确认重置')}
            </Button>
          </>
        }
      >
        <p>{name}</p>
      </Dialog>
    </>
  )
}
