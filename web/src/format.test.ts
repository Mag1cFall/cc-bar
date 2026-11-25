import assert from 'node:assert/strict'
import { test } from 'node:test'
import { dateTime, quotaResetTime } from './format.ts'
import type { QuotaLimit } from './models'

// weeklyQuota 构造当前由其他窗口起约束作用的周额度
function weeklyQuota(usedPercent: number, resetsAt?: string): QuotaLimit {
  return { id: 'weekly', kind: 1, isActive: false, window: { usedPercent, resetsAt } }
}

await test('周额度存在重置时间时保留相对和绝对日期', () => {
  const now = new Date('2026-10-05T04:00:00+08:00').valueOf()
  for (const quota of [
    weeklyQuota(88, '2026-10-06T09:00:00+08:00'),
    weeklyQuota(4, '2026-10-07T05:00:00+08:00'),
  ]) {
    assert.match(quotaResetTime(quota, false, false, now), /^\d+天 \d+小时$/)
    assert.equal(quotaResetTime(quota, false, true, now), dateTime(quota.window.resetsAt, false, true))
  }
})

await test('已有用量而缺失重置时间时显示待更新', () => {
  assert.equal(quotaResetTime(weeklyQuota(88), false, false), '重置时间待更新')
  assert.equal(quotaResetTime(weeklyQuota(4, 'invalid'), true, true), 'Reset time pending')
})

await test('零用量无重置时间的窗口显示尚未激活', () => {
  assert.equal(quotaResetTime(weeklyQuota(0), false, false), '尚未激活')
  assert.equal(quotaResetTime(undefined, false, false), '—')
})
