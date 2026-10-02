import { useState } from 'react'
import { Check } from 'lucide-react'
import { Button, Dialog, Logo, SettingRow, Switch } from './components'
import { useApp } from './context'
import { quotaKeys } from './format'

export default function Onboarding() {
  const { snapshot, tr, save, busy } = useApp()
  const [step, setStep] = useState(0)
  const settings = snapshot.settings
  const complete = () => {
    void save({ ...settings, didCompleteOnboarding: true })
  }
  return (
    <Dialog
      open={!settings.didCompleteOnboarding}
      onOpenChange={(open) => {
        if (!open) complete()
      }}
      title={
        step === 0
          ? tr('Welcome to CCBar', '欢迎使用 CCBar')
          : step === 1
            ? tr('Detected accounts', '已检测到的账号')
            : step === 2
              ? tr('Make it yours', '配置显示方式')
              : tr('Ready to go', '准备就绪')
      }
      footer={
        <>
          <div className="onboarding-dots">
            {[0, 1, 2, 3].map((value) => (
              <span key={value} className={value === step ? 'active' : ''} />
            ))}
          </div>
          {step > 0 && <Button onClick={() => setStep(step - 1)}>{tr('Back', '上一步')}</Button>}
          <Button tone="primary" disabled={busy} onClick={() => (step < 3 ? setStep(step + 1) : complete())}>
            {step === 0
              ? tr('Get started', '开始')
              : step === 3
                ? tr('Done', '完成')
                : tr('Continue', '继续')}
          </Button>
        </>
      }
    >
      {step === 0 ? (
        <div className="onboarding-welcome">
          <img src="./ccbar-icon.png" alt="CCBar" />
          <p>
            {tr(
              'Your AI subscription quotas, account switcher and local usage at a glance.',
              '随时查看 AI 订阅额度、切换账号与追踪本地用量',
            )}
          </p>
        </div>
      ) : step === 1 ? (
        <div className="detected-list">
          {snapshot.providers.map((provider) => (
            <div key={provider.app}>
              <Logo name={quotaKeys[provider.app] ?? ''} />
              <span>
                <strong>{provider.name}</strong>
                <p>{provider.account?.email ?? provider.account?.login ?? tr('Not detected', '未检测到')}</p>
              </span>
              {provider.account && <Check size={16} />}
            </div>
          ))}
        </div>
      ) : step === 2 ? (
        <>
          <SettingRow title={tr('Show floating HUD', '显示桌面悬浮窗')}>
            <Switch
              checked={settings.floatingEnabled}
              label={tr('Show floating HUD', '显示桌面悬浮窗')}
              disabled={busy}
              onCheckedChange={(value) => {
                void save({ ...settings, floatingEnabled: value })
              }}
            />
          </SettingRow>
          <SettingRow title={tr('Privacy mode', '隐私模式')}>
            <Switch
              checked={settings.privacyMode}
              label={tr('Privacy mode', '隐私模式')}
              disabled={busy}
              onCheckedChange={(value) => {
                void save({ ...settings, privacyMode: value })
              }}
            />
          </SettingRow>
          <SettingRow title={tr('Launch at login', '开机自动启动')}>
            <Switch
              checked={settings.launchAtLogin}
              label={tr('Launch at login', '开机自动启动')}
              disabled={busy}
              onCheckedChange={(value) => {
                void save({ ...settings, launchAtLogin: value })
              }}
            />
          </SettingRow>
        </>
      ) : (
        <div className="onboarding-welcome">
          <Check size={50} />
          <p>
            {tr(
              'CCBar stays in your notification area. Click its icon for quotas, or open Statistics and Settings from its menu.',
              'CCBar 常驻通知区域，点击图标查看额度，通过菜单打开统计与设置',
            )}
          </p>
        </div>
      )}
    </Dialog>
  )
}
