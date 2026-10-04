package accounts

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/providers"
	"github.com/google/uuid"
)

// checkpointDesktop 保存当前 Desktop 的完整账号会话
func (store *Store) checkpointDesktop(desktop *desktopApp) (*ClaudeProfile, error) {
	identity := desktopIdentity(desktop.Directory)
	if identity == "" {
		return nil, nil
	}
	credential, _ := providers.ReadClaudeDesktopDirectory(desktop.Directory, nil)
	if credential != nil && credential.Source != "Claude Desktop Code" {
		credential = nil
	}
	if !hasCompleteCodeLogin(credential) || credential.AccountUUID != identity || !desktopWebSession(desktop.Directory) {
		return nil, errors.New("请完成 Desktop 的网页与 Code 登录后再保存账号")
	}
	store.mu.Lock()
	profile := ClaudeProfile{ID: strings.ReplaceAll(uuid.NewString(), "-", ""), AccountUUID: identity, Name: "Claude " + identity[:min(8, len(identity))], NeedsLogin: true}
	index := -1
	for i, saved := range store.claude {
		if saved.AccountUUID == identity {
			profile, index = saved, i
			break
		}
	}
	store.mu.Unlock()
	if credential.Email == "" {
		credential.Email = profile.Email
	}
	if credential.SubscriptionType == "" {
		credential.SubscriptionType = profile.Plan
	}
	if credential.Email == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := providers.FetchClaudeProfile(ctx, credential); err != nil {
			return nil, err
		}
	}
	profile.DesktopDirectory, profile.DesktopSaved, profile.DesktopLinked = desktop.Directory, true, true
	if err := saveDesktopSession(desktop.Directory, store.desktopSession(profile)); err != nil {
		return nil, err
	}
	if !store.isManaged(profile) {
		profile.ConfigDirectory = filepath.Join(store.dataDir, "claude-accounts", profile.ID)
		profile.UsesDefaultConfig = false
	}
	if credential != nil {
		if err := prepareHistory(profile.ConfigDirectory, filepath.Join(store.home, ".claude")); err != nil {
			return nil, err
		}
		if err := store.writeDesktopClaude(profile, credential); err != nil {
			return nil, err
		}
		profile.NeedsLogin = false
		profile.Email, profile.OrganizationUUID = credential.Email, credential.OrganizationUUID
		profile.Plan = credential.SubscriptionType
		if credential.Email != "" && index < 0 {
			profile.Name = strings.Split(credential.Email, "@")[0]
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if index < 0 {
		store.claude = append(store.claude, profile)
	} else {
		store.claude[index] = profile
	}
	if err := store.prunePartialClaude(); err != nil {
		return nil, err
	}
	return &profile, store.saveClaudeLocked()
}

// saveCurrentDesktop 保存原生登录的网页会话与 Code 凭据
func (store *Store) saveCurrentDesktop(desktop *desktopApp) (profile *ClaudeProfile, result error) {
	attempt, err := store.beginLogin("", "save", 90*time.Second)
	if err != nil {
		return nil, err
	}
	defer attempt.cancel()
	defer func() { store.finishLogin(attempt, result) }()
	credential, _ := providers.ReadClaudeDesktopDirectory(desktop.Directory, nil)
	if credential == nil || credential.Source != "Claude Desktop Code" {
		store.loginStage(attempt, "linking", nil)
		if desktop.ProcessID == 0 {
			if err := desktop.launch(); err != nil {
				return nil, err
			}
		}
		command := execDesktopCode(desktop.Executable)
		if err := command.Start(); err != nil {
			return nil, err
		}
		go command.Wait()
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for credential == nil || credential.Source != "Claude Desktop Code" {
			select {
			case <-attempt.ctx.Done():
				if errors.Is(attempt.ctx.Err(), context.Canceled) {
					return nil, attempt.ctx.Err()
				}
				return nil, errors.New("请在 Desktop 的 Code 页面完成登录后再保存")
			case <-ticker.C:
				credential, _ = providers.ReadClaudeDesktopDirectory(desktop.Directory, nil)
			}
		}
	}
	store.loginStage(attempt, "closing", nil)
	attempt.desktop = desktop
	if err := desktop.close(attempt.ctx); err != nil {
		return nil, err
	}
	store.loginStage(attempt, "saving", nil)
	profile, result = store.checkpointDesktop(desktop)
	if result != nil {
		return nil, result
	}
	if profile == nil {
		return nil, errors.New("Desktop 当前账号尚未登录")
	}
	store.mu.Lock()
	attempt.AccountID = profile.ID
	store.mu.Unlock()
	store.loginStage(attempt, "reloading", nil)
	if err := syncDesktopCodeHistory(desktop.Directory, *profile, store.ListClaude()); err != nil {
		return nil, err
	}
	return profile, desktop.launch()
}

// switchDesktop 保存当前会话并载入所选账号的网页与 Code 登录
func (store *Store) switchDesktop(attempt *loginAttempt, profile ClaudeProfile) error {
	desktop, err := store.desktopApp(attempt.ctx)
	if err != nil || desktop == nil {
		return err
	}
	current := desktopIdentity(desktop.Directory)
	if current != profile.AccountUUID {
		if !store.HasDesktopSession(profile) {
			return errors.New("此账号的 Desktop 授权尚未完整，请重新登录")
		}
		if desktopIdentity(store.desktopSession(profile)) != profile.AccountUUID {
			return errors.New("已保存的 Desktop 登录与此账号不一致，请重新登录")
		}
	}
	store.loginStage(attempt, "closing", nil)
	attempt.desktop = desktop
	if err := desktop.close(attempt.ctx); err != nil {
		return err
	}
	previous, err := store.checkpointDesktop(desktop)
	if err != nil {
		return err
	}
	attempt.previous = previous
	if err := attempt.ctx.Err(); err != nil {
		return err
	}
	store.loginStage(attempt, "restoring", nil)
	if current != profile.AccountUUID {
		attempt.desktopChanged = true
		if err := restoreDesktopSession(desktop.Directory, store.desktopSession(profile)); err != nil {
			return err
		}
	}
	if err := attempt.ctx.Err(); err != nil {
		return err
	}
	store.loginStage(attempt, "reloading", nil)
	if err := syncDesktopCodeHistory(desktop.Directory, profile, store.ListClaude()); err != nil {
		return err
	}
	if err := desktop.launch(); err != nil {
		return err
	}
	if desktopIdentity(desktop.Directory) != profile.AccountUUID {
		return errors.New("Desktop 账号尚未切换到所选账号")
	}
	store.mu.Lock()
	if index := store.claudeIndex(profile.ID); index >= 0 {
		store.claude[index].DesktopDirectory = desktop.Directory
		store.claude[index].DesktopSaved = true
		store.claude[index].DesktopLinked = true
	}
	err = store.saveClaudeLocked()
	store.mu.Unlock()
	return err
}

// HasDesktopSession 检查保存的 Desktop 会话是否完整
func (store *Store) HasDesktopSession(profile ClaudeProfile) bool {
	if !profile.DesktopSaved || desktopIdentity(store.desktopSession(profile)) != profile.AccountUUID {
		return false
	}
	credential, err := store.readClaude(profile)
	if err != nil || !hasCompleteCodeLogin(credential) || credential.AccountUUID != profile.AccountUUID {
		return false
	}
	return desktopWebSession(store.desktopSession(profile))
}
