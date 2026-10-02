import { useDeferredValue, useState } from 'react'
import { Clipboard as WailsClipboard } from '@wailsio/runtime'
import { Copy, Search } from 'lucide-react'
import { api } from '../bridge'
import { Button, Card, Empty, Logo, QueryState, Section, Select } from '../components'
import { useApp } from '../context'
import { compact, dateTime, money, usageKeys, usageNames } from '../format'
import { useQuery } from '../hooks'
import type { ConversationQuery, UsageQuery } from '../models'
import { TokenBreakdown } from './shared'

export default function Conversations({ query }: { query: UsageQuery }) {
  const { tr, english, revision } = useApp()
  const [search, setSearch] = useState('')
  const [project, setProject] = useState('*')
  const [sort, setSort] = useState('recent')
  const [offset, setOffset] = useState(0)
  const [selected, setSelected] = useState<string>()
  const scope = JSON.stringify([query.service, query.from, query.to])
  const [previousScope, setPreviousScope] = useState(scope)
  if (scope !== previousScope) {
    setPreviousScope(scope)
    setProject('*')
    setOffset(0)
    setSelected(undefined)
  }
  const deferredSearch = useDeferredValue(search)
  const request: ConversationQuery = {
    app: query.service,
    from: query.from,
    to: query.to,
    search: deferredSearch,
    project: project === '*' ? null : project === '__none__' ? '' : project,
    sort,
    offset,
    limit: 25,
  }
  const result = useQuery(`${JSON.stringify(request)}:${revision}`, () => api.conversations(request))
  const items = result.data?.items ?? []
  const selectedId = items.find((item) => item.id === selected)?.id ?? items[0]?.id
  const change = (action: () => void) => {
    action()
    setOffset(0)
    setSelected(undefined)
  }
  return (
    <>
      <div className="conversation-toolbar">
        <label className="search-field">
          <Search size={15} />
          <input
            value={search}
            aria-label={tr('Search conversations', '搜索对话')}
            placeholder={tr('Search conversations', '搜索对话')}
            onChange={(event) => change(() => setSearch(event.target.value))}
          />
        </label>
        <Select
          label={tr('Project', '项目')}
          value={project}
          onChange={(value) => change(() => setProject(value))}
          options={[
            { value: '*', label: tr('All projects', '全部项目') },
            { value: '__none__', label: tr('No project', '无项目') },
            ...(result.data?.projects ?? [])
              .filter((item) => item.id)
              .map((item) => ({ value: item.id, label: `${item.name} (${item.count})` })),
          ]}
        />
        <Select
          label={tr('Sort conversations', '对话排序')}
          value={sort}
          onChange={(value) => change(() => setSort(value))}
          options={[
            { value: 'recent', label: tr('Recent', '最近') },
            { value: 'tokens', label: 'Tokens' },
            { value: 'cost', label: tr('Cost', '费用') },
            { value: 'title', label: tr('Title', '标题') },
            { value: 'oldest', label: tr('Oldest', '最早') },
          ]}
        />
      </div>
      <QueryState pending={result.pending || search !== deferredSearch} error={result.error} />
      <div className="conversation-columns">
        <div className="conversation-list">
          <div className="result-count">
            {result.data?.total ?? 0} {tr('conversations', '个对话')}
          </div>
          <div className="conversation-items">
            {items.map((item) => (
              <button
                key={item.id}
                className={`conversation-row ${selectedId === item.id ? 'selected' : ''}`}
                onClick={() => setSelected(item.id)}
              >
                <div className="conversation-item-head">
                  <Logo name={usageKeys[item.app] ?? ''} size={18} />
                  <strong className="ellipsis" title={item.title || item.id}>
                    {item.title || item.id}
                  </strong>
                  <span>{money(item.totals.cost)}</span>
                </div>
                <p className="ellipsis" title={item.cwd}>
                  {item.project || tr('No project', '无项目')}
                  {item.branch ? ` · ${item.branch}` : ''}
                </p>
                <div className="conversation-item-foot">
                  <span>{compact(item.totals.tokens, english)} Tokens</span>
                  <span>{dateTime(item.endedAt, english, true)}</span>
                </div>
              </button>
            ))}
            {!result.pending && items.length === 0 && (
              <Empty title={tr('No matching conversations', '暂无匹配对话')} />
            )}
          </div>
          <div className="pagination">
            <Button
              disabled={offset === 0 || result.pending}
              onClick={() => {
                setOffset(Math.max(0, offset - 25))
                setSelected(undefined)
              }}
            >
              {tr('Previous', '上一页')}
            </Button>
            <span>
              {result.data?.total
                ? `${offset + 1}–${Math.min(offset + 25, result.data.total)} / ${result.data.total}`
                : '0'}
            </span>
            <Button
              disabled={offset + 25 >= (result.data?.total ?? 0) || result.pending}
              onClick={() => {
                setOffset(offset + 25)
                setSelected(undefined)
              }}
            >
              {tr('Next', '下一页')}
            </Button>
          </div>
        </div>
        <div className="conversation-detail">
          {selectedId ? (
            <Details id={selectedId} />
          ) : (
            <Empty title={tr('Select a conversation', '选择一个对话')} />
          )}
        </div>
      </div>
    </>
  )
}

function Details({ id }: { id: string }) {
  const { tr, english, revision, run } = useApp()
  const [showEvents, setShowEvents] = useState(false)
  const result = useQuery(`${id}:${revision}`, () => api.conversation(id))
  const detail = result.data,
    item = detail?.conversation
  const totals = item?.totals
  return (
    <>
      <QueryState pending={result.pending} error={result.error} />
      {detail && item && totals && (
        <>
          <div className="detail-heading">
            <Logo name={usageKeys[item.app] ?? ''} size={26} />
            <div>
              <h2>{item.title || tr('Conversation', '对话')}</h2>
              <p>
                {usageNames[item.app]} · {item.speed || 'standard'}
              </p>
            </div>
          </div>
          <div className="conversation-id">
            <code className="ellipsis" title={id}>
              {id}
            </code>
            <Button
              tone="ghost"
              className="icon-button"
              title={tr('Copy ID', '复制 ID')}
              aria-label={tr('Copy ID', '复制 ID')}
              onClick={() =>
                run(
                  async () => {
                    await WailsClipboard.SetText(id)
                  },
                  tr('ID copied', 'ID 已复制'),
                )
              }
            >
              <Copy size={14} />
            </Button>
          </div>
          <div className="conversation-meta">
            <div>
              <span>{tr('Started', '开始')}</span>
              <strong>{dateTime(item.startedAt, english)}</strong>
            </div>
            <div>
              <span>{tr('Last activity', '最后活动')}</span>
              <strong>{dateTime(item.endedAt, english)}</strong>
            </div>
            {item.cwd && (
              <div>
                <span>{tr('Directory', '目录')}</span>
                <strong title={item.cwd}>{item.cwd}</strong>
              </div>
            )}
            {item.branch && (
              <div>
                <span>{tr('Branch', '分支')}</span>
                <strong>{item.branch}</strong>
              </div>
            )}
            {item.subtasks && <span className="badge">{tr('Includes subtasks', '含子任务')}</span>}
          </div>
          <Card className="padded token-hero">
            <div className="token-total">
              <span>{tr('Total tokens', '总 Tokens')}</span>
              <strong className="numeric">{compact(totals.tokens, english)}</strong>
            </div>
            <TokenBreakdown totals={totals} extended />
          </Card>
          <Section title={tr('Cost breakdown', '费用拆分')}>
            <Card className="padded">
              <div className="cost-grid">
                {Object.entries(detail.costs).map(([key, value]) => (
                  <div key={key}>
                    <span>
                      {key === 'input'
                        ? tr('Input', '输入')
                        : key === 'output'
                          ? tr('Output', '输出')
                          : key === 'cacheRead'
                            ? tr('Cache read', '缓存读取')
                            : tr('Cache write', '缓存写入')}
                    </span>
                    <strong>{money(value)}</strong>
                  </div>
                ))}
              </div>
            </Card>
          </Section>
          <Section title={tr('Models & speed', '模型与档位')}>
            <Card className="padded">
              {(detail.models ?? []).map((model) => (
                <div className="detail-model" key={model.id}>
                  <div className="summary-heading">
                    <strong>{model.name}</strong>
                    <span className="badge">{model.speed || 'standard'}</span>
                    <strong>{money(model.totals.cost)}</strong>
                  </div>
                  <TokenBreakdown totals={model.totals} extended />
                </div>
              ))}
            </Card>
          </Section>
          <Section
            title={tr('Requests', '请求明细')}
            actions={
              <Button tone="ghost" onClick={() => setShowEvents(!showEvents)}>
                {showEvents ? tr('Collapse', '收起') : tr('Show', '展开')}
              </Button>
            }
          >
            {showEvents && (
              <Card className="table-card">
                <table>
                  <thead>
                    <tr>
                      <th>{tr('Time', '时间')}</th>
                      <th>{tr('Model', '模型')}</th>
                      <th>Tokens</th>
                      <th>{tr('Cost', '费用')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(detail.events ?? []).map((event) => (
                      <tr key={event.id}>
                        <td>{dateTime(event.time, english, true)}</td>
                        <td>
                          {event.model}
                          <span className="table-caption">{event.speed}</span>
                        </td>
                        <td>
                          {compact(event.input + event.output + event.cacheRead + event.cacheWrite, english)}
                        </td>
                        <td>{event.cost === null ? '—' : money(event.cost)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </Card>
            )}
          </Section>
        </>
      )}
    </>
  )
}
