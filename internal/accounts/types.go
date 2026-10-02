package accounts

import (
	"time"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// ClaudeProfile 保存官方账号目录和展示状态
type ClaudeProfile struct {
	ID                   string               `json:"id"`
	Name                 string               `json:"name"`
	ConfigDirectory      string               `json:"configDirectory"`
	UsesDefaultConfig    bool                 `json:"usesDefaultConfig"`
	DesktopLinked        bool                 `json:"desktopLinked,omitempty"`
	CredentialsUpdatedAt *time.Time           `json:"credentialsUpdatedAt,omitempty"`
	Email                string               `json:"email,omitempty"`
	AccountUUID          string               `json:"accountUuid,omitempty"`
	OrganizationUUID     string               `json:"organizationUuid,omitempty"`
	Plan                 string               `json:"plan,omitempty"`
	Snapshot             *model.QuotaSnapshot `json:"snapshot,omitempty"`
	BackoffUntil         *time.Time           `json:"backoffUntil,omitempty"`
	Error                string               `json:"error,omitempty"`
	NeedsLogin           bool                 `json:"needsLogin"`
	IsLoggingIn          bool                 `json:"isLoggingIn"`
	IsActive             bool                 `json:"isActive"`
	HistoryShared        bool                 `json:"historyShared"`
}

// CodexAccount 表示仅查看额度的导入账号
type CodexAccount struct {
	ID                    string               `json:"id"`
	Alias                 string               `json:"alias"`
	DisplayName           string               `json:"displayName"`
	Email                 string               `json:"email,omitempty"`
	PlanType              string               `json:"planType,omitempty"`
	AccountID             string               `json:"accountId"`
	VisibleInPopover      bool                 `json:"visibleInPopover"`
	IsPersonalAccessToken bool                 `json:"isPersonalAccessToken"`
	Snapshot              *model.QuotaSnapshot `json:"snapshot,omitempty"`
	Error                 string               `json:"error,omitempty"`
	IsRefreshing          bool                 `json:"isRefreshing"`
	ProtectedToken        string               `json:"-"`
	ProtectedRefreshToken string               `json:"-"`
	BackoffUntil          *time.Time           `json:"-"`
}

// CodexPreview 展示导入前解析结果
type CodexPreview struct {
	Email         string `json:"email,omitempty"`
	Plan          string `json:"plan,omitempty"`
	AccountID     string `json:"accountId,omitempty"`
	UserID        string `json:"userId,omitempty"`
	PersonalToken bool   `json:"personalToken"`
}

// ResetCredit 表示可消费的额度重置次数
type ResetCredit struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Status    string     `json:"status"`
	GrantedAt *time.Time `json:"grantedAt,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// ResetCredits 保留服务器可用次数与详细记录
type ResetCredits struct {
	Available int           `json:"available"`
	Credits   []ResetCredit `json:"credits"`
}

// ResetResult 表示一次明确确认的兑换结果
type ResetResult struct {
	Code         string        `json:"code"`
	WindowsReset int           `json:"windowsReset"`
	Credits      *ResetCredits `json:"credits,omitempty"`
}
