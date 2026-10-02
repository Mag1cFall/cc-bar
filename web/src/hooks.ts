import { useEffect, useEffectEvent, useState } from 'react'
import type { CancellablePromise } from '@wailsio/runtime'

const results = new Map<string, unknown>()

// useQuery 复用同一数据版本的查询结果并取消过期请求
export function useQuery<T>(key: string, load: () => CancellablePromise<T>, enabled = true) {
  const [data, setData] = useState<T>(() => results.get(key) as T)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(() => enabled && !results.has(key))
  const read = useEffectEvent(load)
  useEffect(() => {
    if (!enabled || results.has(key)) return
    let current = true
    const request = read()
    const fetch = async () => {
      setPending(true)
      setError('')
      try {
        const result = await request
        if (current) {
          results.set(key, result)
          if (results.size > 40) results.delete(results.keys().next().value!)
          setData(result)
        }
      } catch (cause) {
        if (current) setError(cause instanceof Error ? cause.message : String(cause))
      } finally {
        if (current) setPending(false)
      }
    }
    void fetch()
    return () => {
      current = false
      void request.cancel()
    }
  }, [key, enabled])
  const cached = results.has(key)
  return {
    data: cached ? (results.get(key) as T) : data,
    error: cached ? '' : error,
    pending: enabled && !cached && pending,
  }
}
