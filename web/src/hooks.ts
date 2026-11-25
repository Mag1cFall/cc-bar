import { useEffect, useEffectEvent, useState, useSyncExternalStore } from 'react'
import type { CancellablePromise } from '@wailsio/runtime'

const results = new Map<string, unknown>()

function subscribeVisibility(callback: () => void) {
  document.addEventListener('visibilitychange', callback)
  window.addEventListener('ccbar:visibility', callback)
  return () => {
    document.removeEventListener('visibilitychange', callback)
    window.removeEventListener('ccbar:visibility', callback)
  }
}

// isPageVisible 合并浏览器可见性与原生窗口显隐状态
export function isPageVisible(): boolean {
  return !document.hidden && (window as { __ccbarVisible?: boolean }).__ccbarVisible !== false
}

// usePageVisible 监听原生隐藏窗口与可见状态切换以消除后台空转
export function usePageVisible(): boolean {
  return useSyncExternalStore(subscribeVisibility, isPageVisible, () => true)
}

// useQuery 复用同一数据版本的查询结果并在隐藏或过期时取消请求
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
          if (results.size > 64) results.delete(results.keys().next().value!)
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
