package model

import "time"

// QuotaApp 标识额度服务并保持已有数据的枚举值
type QuotaApp int

const (
	Codex QuotaApp = iota
	Claude
	Antigravity
	Cursor
	CommandCode
)

// UsageApp 标识本地用量来源
type UsageApp int

const (
	UsageCodex UsageApp = iota
	UsageClaude
	UsageCursor
	UsagePi
	UsageOpencode
	UsageDsh
	UsageOmp
)

// LimitKind 标识额度窗口
type LimitKind int

const (
	FiveHour LimitKind = iota
	Weekly
	ModelWeekly
	UnknownLimit
)

// Credential 保留凭据及身份且只向界面传递展示字段
type Credential struct {
	AccessToken           string     `json:"-"`
	RefreshToken          string     `json:"-"`
	Scopes                []string   `json:"-"`
	RateLimitTier         string     `json:"-"`
	Email                 string     `json:"email,omitempty"`
	AccountUUID           string     `json:"accountUuid,omitempty"`
	OrganizationUUID      string     `json:"organizationUuid,omitempty"`
	AccountID             string     `json:"accountId,omitempty"`
	UserID                string     `json:"userId,omitempty"`
	SubscriptionType      string     `json:"subscriptionType,omitempty"`
	Login                 string     `json:"login,omitempty"`
	IsPersonalAccessToken bool       `json:"isPersonalAccessToken"`
	ExpiresAt             *time.Time `json:"expiresAt,omitempty"`
	LastRefresh           *time.Time `json:"lastRefresh,omitempty"`
	Source                string     `json:"source"`
}

// QuotaWindow 表示已用比例与重置时间
type QuotaWindow struct {
	UsedPercent   float64    `json:"usedPercent"`
	ResetsAt      *time.Time `json:"resetsAt,omitempty"`
	WindowSeconds *int       `json:"windowSeconds,omitempty"`
}

// QuotaLimit 表示服务的一条额度限制
type QuotaLimit struct {
	ID          string      `json:"id"`
	Kind        LimitKind   `json:"kind"`
	DisplayName string      `json:"displayName,omitempty"`
	Window      QuotaWindow `json:"window"`
	IsActive    *bool       `json:"isActive,omitempty"`
}

// QuotaSnapshot 保留原有主额度、辅助额度及模型额度
type QuotaSnapshot struct {
	App             QuotaApp     `json:"app"`
	PrimaryLimit    *QuotaLimit  `json:"primaryLimit,omitempty"`
	SecondaryLimit  *QuotaLimit  `json:"secondaryLimit,omitempty"`
	AuxiliaryLimits []QuotaLimit `json:"auxiliaryLimits"`
	ModelLimits     []QuotaLimit `json:"modelLimits"`
	GeminiWindow    *QuotaWindow `json:"geminiWindow,omitempty"`
	GeminiWeekly    *QuotaWindow `json:"geminiWeekly,omitempty"`
	IsUnlimited     *bool        `json:"isUnlimited,omitempty"`
	PlanType        string       `json:"planType,omitempty"`
	FetchedAt       time.Time    `json:"fetchedAt"`
}

// AllLimits 返回全部额度窗口
func (snapshot QuotaSnapshot) AllLimits() []QuotaLimit {
	limits := make([]QuotaLimit, 0, 2+len(snapshot.AuxiliaryLimits)+len(snapshot.ModelLimits))
	if snapshot.PrimaryLimit != nil {
		limits = append(limits, *snapshot.PrimaryLimit)
	}
	if snapshot.SecondaryLimit != nil {
		limits = append(limits, *snapshot.SecondaryLimit)
	}
	limits = append(limits, snapshot.AuxiliaryLimits...)
	return append(limits, snapshot.ModelLimits...)
}

// ProviderState 保存一个服务的展示状态
type ProviderState struct {
	App           QuotaApp       `json:"app"`
	Name          string         `json:"name"`
	Account       *Credential    `json:"account,omitempty"`
	Snapshot      *QuotaSnapshot `json:"snapshot,omitempty"`
	Error         string         `json:"error,omitempty"`
	ErrorKind     string         `json:"errorKind,omitempty"`
	Source        string         `json:"source,omitempty"`
	LastSuccessAt *time.Time     `json:"lastSuccessAt,omitempty"`
	Refreshing    bool           `json:"refreshing"`
}

// ProviderDisplaySettings 保存服务开关与展示位置
type ProviderDisplaySettings struct {
	Enabled     bool `json:"enabled"`
	MenuBar     bool `json:"menuBar"`
	FloatingHud bool `json:"floatingHud"`
}

// Settings 沿用 Windows 数据目录中的设置字段
type Settings struct {
	Providers                       map[QuotaApp]ProviderDisplaySettings `json:"providers"`
	UsageVisibility                 map[UsageApp]bool                    `json:"usageVisibility"`
	FloatingEnabled                 bool                                 `json:"floatingEnabled"`
	DidCompleteOnboarding           bool                                 `json:"didCompleteOnboarding"`
	LaunchAtLogin                   bool                                 `json:"launchAtLogin"`
	PrivacyMode                     bool                                 `json:"privacyMode"`
	ShowServiceStatus               bool                                 `json:"showServiceStatus"`
	VerboseLogging                  bool                                 `json:"verboseLogging"`
	QuotaIntervalMinutes            int                                  `json:"quotaIntervalMinutes"`
	UsageIntervalMinutes            int                                  `json:"usageIntervalMinutes"`
	MenuBarWindow                   string                               `json:"menuBarWindow"`
	ResetTimeDisplay                string                               `json:"resetTimeDisplay"`
	Language                        string                               `json:"language"`
	Theme                           string                               `json:"theme"`
	CommandCodeCredentialPreference string                               `json:"commandCodeCredentialPreference"`
	HudLeft                         *float64                             `json:"hudLeft,omitempty"`
	HudTop                          *float64                             `json:"hudTop,omitempty"`
}

// DefaultSettings 提供首次启动的服务与显示配置
func DefaultSettings() Settings {
	settings := Settings{Providers: map[QuotaApp]ProviderDisplaySettings{}, UsageVisibility: map[UsageApp]bool{},
		PrivacyMode: true, ShowServiceStatus: true, QuotaIntervalMinutes: 2, UsageIntervalMinutes: 5,
		MenuBarWindow: "primary", ResetTimeDisplay: "relative", Language: "system", Theme: "system", CommandCodeCredentialPreference: "automatic"}
	for app := Codex; app <= CommandCode; app++ {
		settings.Providers[app] = ProviderDisplaySettings{Enabled: app != Cursor && app != CommandCode, MenuBar: app != Cursor, FloatingHud: app != Cursor}
	}
	for app := UsageCodex; app <= UsageOmp; app++ {
		settings.UsageVisibility[app] = app != UsageCursor
	}
	return settings
}

// QuotaName 返回服务名称
func QuotaName(app QuotaApp) string {
	names := [...]string{"Codex", "Claude Code", "Antigravity", "Cursor", "Command Code"}
	if app < Codex || app > CommandCode {
		return "Unknown"
	}
	return names[app]
}
