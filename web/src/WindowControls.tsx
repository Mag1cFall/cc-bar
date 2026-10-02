import { useEffect, useState } from 'react'
import { Window } from '@wailsio/runtime'
import { Minus, Square, Copy, X } from 'lucide-react'
import { useApp } from './context'

// WindowControls 使用 Wails 原生窗口操作与 Windows 标题栏命中区域
export default function WindowControls() {
  const { tr } = useApp()
  const [maximised, setMaximised] = useState(false)
  useEffect(() => {
    const resized = () => {
      void Window.IsMaximised().then(setMaximised).catch(console.error)
    }
    window.addEventListener('resize', resized)
    return () => window.removeEventListener('resize', resized)
  }, [])
  return (
    <div className="window-controls">
      <button
        className="window-minimise"
        title={tr('Minimise', '最小化')}
        aria-label={tr('Minimise', '最小化')}
        onClick={() => Window.Minimise()}
      >
        <Minus size={15} />
      </button>
      <button
        className="window-maximise"
        title={maximised ? tr('Restore', '还原窗口') : tr('Maximise', '最大化')}
        aria-label={maximised ? tr('Restore', '还原窗口') : tr('Maximise', '最大化')}
        onClick={() => Window.ToggleMaximise()}
      >
        {maximised ? <Copy size={13} /> : <Square size={13} />}
      </button>
      <button
        className="window-close"
        title={tr('Close window', '关闭窗口')}
        aria-label={tr('Close window', '关闭窗口')}
        onClick={() => Window.Close()}
      >
        <X size={17} />
      </button>
    </div>
  )
}
