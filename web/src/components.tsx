import type { ButtonHTMLAttributes, ReactNode } from 'react'
import * as DialogPrimitive from '@radix-ui/react-dialog'
import * as DropdownPrimitive from '@radix-ui/react-dropdown-menu'
import * as SelectPrimitive from '@radix-ui/react-select'
import * as SwitchPrimitive from '@radix-ui/react-switch'
import { Check, ChevronDown, ChevronUp, MoreHorizontal, X, LoaderCircle, AlertTriangle } from 'lucide-react'
import { useApp } from './context'
import { dateTime, quotaTone, relativeTime, remaining } from './format'
import type { QuotaLimit } from './models'

export function Button({
  children,
  className = '',
  tone,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { tone?: 'primary' | 'danger' | 'ghost' }) {
  return (
    <button type="button" className={`button ${tone ?? ''} ${className}`} {...props}>
      {children}
    </button>
  )
}
export function Switch({
  checked,
  onCheckedChange,
  label,
  disabled,
}: {
  checked: boolean
  onCheckedChange: (value: boolean) => void
  label: string
  disabled?: boolean
}) {
  return (
    <SwitchPrimitive.Root
      className="switch"
      checked={checked}
      onCheckedChange={onCheckedChange}
      aria-label={label}
      disabled={disabled}
    >
      <SwitchPrimitive.Thumb className="switch-thumb" />
    </SwitchPrimitive.Root>
  )
}
export function Select({
  value,
  onChange,
  options,
  label,
  className = '',
  disabled = false,
  icon,
}: {
  value: string
  onChange: (value: string) => void
  options: { value: string; label: string }[]
  label: string
  className?: string
  disabled?: boolean
  icon?: ReactNode
}) {
  return (
    <SelectPrimitive.Root value={value} onValueChange={onChange} disabled={disabled}>
      <SelectPrimitive.Trigger className={`select ${className}`} aria-label={label}>
        <span className="select-value">
          {icon}
          <SelectPrimitive.Value />
        </span>
        <SelectPrimitive.Icon className="select-chevron">
          <ChevronDown size={15} />
        </SelectPrimitive.Icon>
      </SelectPrimitive.Trigger>
      <SelectPrimitive.Portal>
        <SelectPrimitive.Content
          className="select-menu"
          position="popper"
          align="end"
          sideOffset={6}
          collisionPadding={10}
        >
          <SelectPrimitive.ScrollUpButton className="select-scroll">
            <ChevronUp size={15} />
          </SelectPrimitive.ScrollUpButton>
          <SelectPrimitive.Viewport className="select-viewport">
            {options.map((option) => (
              <SelectPrimitive.Item key={option.value} value={option.value} className="select-option">
                <SelectPrimitive.ItemIndicator className="select-check">
                  <Check size={14} />
                </SelectPrimitive.ItemIndicator>
                <SelectPrimitive.ItemText>{option.label}</SelectPrimitive.ItemText>
              </SelectPrimitive.Item>
            ))}
          </SelectPrimitive.Viewport>
          <SelectPrimitive.ScrollDownButton className="select-scroll">
            <ChevronDown size={15} />
          </SelectPrimitive.ScrollDownButton>
        </SelectPrimitive.Content>
      </SelectPrimitive.Portal>
    </SelectPrimitive.Root>
  )
}
export function Card({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <section className={`card ${className}`}>{children}</section>
}
export function Section({
  title,
  description,
  children,
  actions,
}: {
  title: string
  description?: string
  children: ReactNode
  actions?: ReactNode
}) {
  return (
    <section className="section">
      <div className="section-heading">
        <div>
          <h2>{title}</h2>
          {description && <p>{description}</p>}
        </div>
        {actions}
      </div>
      {children}
    </section>
  )
}
export function SettingRow({
  title,
  description,
  children,
}: {
  title: string
  description?: string
  children: ReactNode
}) {
  return (
    <div className="setting-row">
      <div className="setting-copy">
        <div className="setting-title">{title}</div>
        {description && <p>{description}</p>}
      </div>
      <div className="setting-control">{children}</div>
    </div>
  )
}
export function Dialog({
  open,
  onOpenChange,
  title,
  description,
  children,
  footer,
}: {
  open: boolean
  onOpenChange: (value: boolean) => void
  title: string
  description?: string
  children: ReactNode
  footer?: ReactNode
}) {
  const { tr } = useApp()
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="dialog-overlay" />
        <DialogPrimitive.Content className="dialog">
          <div className="dialog-heading">
            <DialogPrimitive.Title>{title}</DialogPrimitive.Title>
            <DialogPrimitive.Close asChild>
              <Button tone="ghost" className="icon-button" aria-label={tr('Close', '关闭')}>
                <X size={17} />
              </Button>
            </DialogPrimitive.Close>
          </div>
          <DialogPrimitive.Description className={description ? 'dialog-description' : 'sr-only'}>
            {description ?? title}
          </DialogPrimitive.Description>
          <div className="dialog-body">{children}</div>
          {footer && <div className="dialog-footer">{footer}</div>}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}
export function ActionMenu({
  label,
  children,
  trigger,
}: {
  label: string
  children: ReactNode
  trigger?: ReactNode
}) {
  return (
    <DropdownPrimitive.Root>
      <DropdownPrimitive.Trigger asChild>
        {trigger ?? (
          <Button tone="ghost" className="icon-button" aria-label={label}>
            <MoreHorizontal size={18} />
          </Button>
        )}
      </DropdownPrimitive.Trigger>
      <DropdownPrimitive.Portal>
        <DropdownPrimitive.Content className="action-menu" align="end" sideOffset={5}>
          {children}
        </DropdownPrimitive.Content>
      </DropdownPrimitive.Portal>
    </DropdownPrimitive.Root>
  )
}
export function MenuItem({
  children,
  onSelect,
  danger = false,
  disabled = false,
}: {
  children: ReactNode
  onSelect: () => void
  danger?: boolean
  disabled?: boolean
}) {
  return (
    <DropdownPrimitive.Item
      className={`menu-item ${danger ? 'danger' : ''}`}
      onSelect={onSelect}
      disabled={disabled}
    >
      {children}
    </DropdownPrimitive.Item>
  )
}
export function Logo({ name, size = 34 }: { name: string; size?: number }) {
  return (
    <span className={`service-logo ${name}`} style={{ width: size, height: size }}>
      <img src={`./logos/${name}.svg`} alt="" />
    </span>
  )
}
export function Status({ kind, text }: { kind: 'live' | 'low' | 'empty' | 'muted'; text: string }) {
  return (
    <span className={`status ${kind}`}>
      <span className="status-dot" />
      {text}
    </span>
  )
}
export function Progress({ value, color }: { value: number | undefined; color?: string }) {
  return (
    <div className={`progress ${quotaTone(value)}`}>
      <div style={{ width: `${Math.max(0, Math.min(100, value ?? 0))}%`, background: color }} />
    </div>
  )
}
export function Quota({
  limit,
  title,
  compact = false,
}: {
  limit?: QuotaLimit
  title: string
  compact?: boolean
}) {
  const { english, tr, snapshot } = useApp()
  const value = remaining(limit?.window.usedPercent)
  return (
    <div className={`quota ${compact ? 'compact' : ''}`}>
      <div className="quota-label">
        <span>{title}</span>
        <strong className={quotaTone(value)}>{value === undefined ? '—' : `${value.toFixed(0)}%`}</strong>
      </div>
      <Progress value={value} />
      <div className="quota-caption">
        {limit?.isActive === false
          ? tr('Not activated', '尚未激活')
          : limit?.window.resetsAt
            ? snapshot.settings.resetTimeDisplay === 'absolute'
              ? dateTime(limit.window.resetsAt, english, true)
              : relativeTime(limit.window.resetsAt, english)
            : '—'}
      </div>
    </div>
  )
}
export function Empty({
  title,
  description,
  children,
}: {
  title: string
  description?: string
  children?: ReactNode
}) {
  return (
    <div className="empty-state">
      <h3>{title}</h3>
      {description && <p>{description}</p>}
      {children}
    </div>
  )
}
export function QueryState({ pending, error }: { pending: boolean; error: string }) {
  const { tr } = useApp()
  return error ? (
    <div className="inline-error" role="alert">
      <AlertTriangle size={15} />
      {error}
    </div>
  ) : pending ? (
    <div className="query-state" role="status">
      <LoaderCircle size={14} className="spin" />
      {tr('Loading…', '读取中…')}
    </div>
  ) : (
    <div className="query-state-placeholder" aria-hidden="true" />
  )
}
export function Segments({
  value,
  onChange,
  options,
  label,
}: {
  value: string
  onChange: (value: string) => void
  options: { value: string; label: string }[]
  label: string
}) {
  return (
    <div className="segments" role="group" aria-label={label}>
      {options.map((option) => (
        <button
          key={option.value}
          className={value === option.value ? 'selected' : ''}
          aria-pressed={value === option.value}
          onClick={() => {
            if (value !== option.value) onChange(option.value)
          }}
        >
          {option.label}
        </button>
      ))}
    </div>
  )
}
