import { createContext, useContext } from 'react'
import type { Settings, Snapshot } from './models'

export interface AppContextValue {
  snapshot: Snapshot
  english: boolean
  tr: (english: string, chinese: string) => string
  refresh: () => Promise<void>
  revision: number
  busy: boolean
  run: (operation: () => Promise<unknown>, success?: string) => Promise<boolean>
  save: (settings: Settings) => Promise<boolean>
  navigate: (page: string) => void
}
export const AppContext = createContext<AppContextValue | null>(null)
export function useApp(): AppContextValue {
  const value = useContext(AppContext)
  if (!value) throw new Error('CCBar context is missing')
  return value
}
