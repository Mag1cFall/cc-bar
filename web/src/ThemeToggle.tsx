import { useState, type MouseEvent } from 'react'
import { Moon, Sun } from 'lucide-react'
import { useApp } from './context'

// ThemeToggle 从按钮中心展开或收起整页主题并保存选择
export default function ThemeToggle() {
  const { snapshot, save, tr } = useApp()
  const [changing, setChanging] = useState(false)
  const dark =
    snapshot.settings.theme === 'dark' ||
    (snapshot.settings.theme === 'system' && matchMedia('(prefers-color-scheme: dark)').matches)
  const label = dark
    ? tr('Switch to light theme', '切换浅色模式')
    : tr('Switch to dark theme', '切换深色模式')

  async function toggle(event: MouseEvent<HTMLButtonElement>) {
    const bounds = event.currentTarget.getBoundingClientRect()
    const x = bounds.x + bounds.width / 2
    const y = bounds.y + bounds.height / 2
    const radius = Math.hypot(Math.max(x, innerWidth - x), Math.max(y, innerHeight - y))
    const theme = dark ? 'light' : 'dark'
    setChanging(true)
    try {
      if (matchMedia('(prefers-reduced-motion: reduce)').matches) {
        await save({ ...snapshot.settings, theme })
        return
      }
      const transition = document.startViewTransition(() => {
        document.documentElement.dataset.theme = theme
      })
      await transition.ready
      const circles = [`circle(0px at ${x}px ${y}px)`, `circle(${radius}px at ${x}px ${y}px)`]
      const animation = document.documentElement.animate(
        { clipPath: dark ? circles.reverse() : circles },
        {
          duration: 420,
          easing: 'ease-in-out',
          fill: 'both',
          pseudoElement: dark ? '::view-transition-old(root)' : '::view-transition-new(root)',
        },
      )
      await animation.finished
      await transition.finished
      animation.cancel()
      await save({ ...snapshot.settings, theme })
    } finally {
      setChanging(false)
    }
  }

  return (
    <button className="theme-toggle" title={label} aria-label={label} disabled={changing} onClick={toggle}>
      {dark ? <Sun size={22} strokeWidth={2.3} /> : <Moon size={22} strokeWidth={2.3} />}
    </button>
  )
}
