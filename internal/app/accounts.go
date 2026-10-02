package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/Mag1cFall/cc-bar/internal/accounts"
	"github.com/Mag1cFall/cc-bar/internal/providers"
)

// SaveClaudeAccount 保存当前 Claude Code 或 Desktop Code 登录
func (service *Service) SaveClaudeAccount() (*accounts.ClaudeProfile, error) {
	profile, err := service.accounts.SaveCurrentClaude()
	service.changed()
	return profile, err
}

// AddClaudeAccount 创建独立的官方 CLI 登录目录
func (service *Service) AddClaudeAccount(name string) (*accounts.ClaudeProfile, error) {
	profile, err := service.accounts.AddClaude(name)
	service.changed()
	return profile, err
}

// LoginClaudeAccount 在终端中运行官方登录流程
func (service *Service) LoginClaudeAccount(ctx context.Context, id string) error {
	err := service.accounts.LoginClaude(ctx, id)
	service.changed()
	service.RefreshQuotas()
	return err
}

// SwitchClaudeAccount 切换官方配置目录并保留共享对话
func (service *Service) SwitchClaudeAccount(id string) error {
	err := service.accounts.SwitchClaude(id)
	if err != nil {
		return err
	}
	service.RefreshQuotas()
	service.changed()
	return nil
}

// StartClaudeAccount 使用选定账号打开 Claude Code
func (service *Service) StartClaudeAccount(id string) error {
	return service.accounts.StartClaude(id, service.home)
}

// RenameClaudeAccount 更新账号名称
func (service *Service) RenameClaudeAccount(id, name string) error {
	err := service.accounts.RenameClaude(id, name)
	service.changed()
	return err
}

// RemoveClaudeAccount 移除列表中的账号
func (service *Service) RemoveClaudeAccount(id string) error {
	err := service.accounts.RemoveClaude(id)
	service.changed()
	return err
}

// RefreshClaudeAccounts 读取各账号最新登录与额度状态
func (service *Service) RefreshClaudeAccounts(ctx context.Context) error {
	err := service.accounts.RefreshClaude(ctx)
	service.changed()
	return err
}

// PreviewCodex 解析粘贴凭据中的账号信息
func (service *Service) PreviewCodex(text string) ([]accounts.CodexPreview, error) {
	return accounts.PreviewCodex(text)
}

// ImportCodex 导入额外账号用于查看额度
func (service *Service) ImportCodex(ctx context.Context, text string, visible bool) (int, error) {
	count, err := service.accounts.ImportCodex(ctx, text, visible)
	service.changed()
	return count, err
}

// SaveCodexAccounts 保存账号别名与弹窗展示设置
func (service *Service) SaveCodexAccounts(values []accounts.CodexAccount) error {
	err := service.accounts.SaveCodexAccounts(values)
	service.changed()
	return err
}

// RemoveCodex 移除已导入账号
func (service *Service) RemoveCodex(id string) error {
	err := service.accounts.RemoveCodex(id)
	service.changed()
	return err
}

// GetResetCredits 读取可用重置次数
func (service *Service) GetResetCredits(ctx context.Context, id string) (*accounts.ResetCredits, error) {
	return service.accounts.FetchResetCredits(ctx, id)
}

// ConsumeResetCredit 执行界面确认后的单次兑换
func (service *Service) ConsumeResetCredit(ctx context.Context, id, creditID string) (*accounts.ResetResult, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	result, err := service.accounts.ConsumeResetCredit(ctx, id, creditID, true, hex.EncodeToString(key))
	service.changed()
	service.RefreshQuotas()
	return result, err
}

// SetCommandCodeKey 保存用户输入的 Command Code 密钥
func (service *Service) SetCommandCodeKey(key string) error {
	err := providers.SaveCommandCodeKey(service.dataDir, key)
	if err == nil {
		service.RefreshQuotas()
	}
	return err
}

// ClearCommandCodeKey 删除手动保存的 Command Code 密钥
func (service *Service) ClearCommandCodeKey() error {
	err := providers.DeleteCommandCodeKey(service.dataDir)
	if err == nil {
		service.RefreshQuotas()
	}
	return err
}
