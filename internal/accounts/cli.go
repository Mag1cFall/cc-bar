package accounts

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Mag1cFall/cc-bar/internal/providers"
)

// ClaudeCLI 保留终端命令与对应 PowerShell 用户配置
type ClaudeCLI struct {
	Executable string
	PowerShell string
}

var claudeLauncherMutex sync.Mutex
var cachedClaudeLauncher *ClaudeCLI

// FindClaude 优先使用用户终端实际解析出的 Claude 命令
func FindClaude(home string) (ClaudeCLI, error) {
	if userHome, err := os.UserHomeDir(); err == nil && sameDirectory(home, userHome) {
		claudeLauncherMutex.Lock()
		defer claudeLauncherMutex.Unlock()
		if cachedClaudeLauncher != nil {
			return *cachedClaudeLauncher, nil
		}
		if cli, found, err := findPowerShellClaude(); found || err != nil {
			if found && err == nil {
				cachedClaudeLauncher = &cli
			}
			return cli, err
		}
	}
	for _, name := range []string{"claude", "claude.ps1"} {
		if path, err := exec.LookPath(name); err == nil {
			return ClaudeCLI{Executable: path}, nil
		}
	}
	return ClaudeCLI{}, errors.New("请将 Claude Code 的启动命令加入 PATH")
}

// cliEnvironment 为官方账号清除会覆盖 OAuth 的第三方登录变量
func cliEnvironment(profile ClaudeProfile) []string {
	remove := map[string]bool{
		"CLAUDE_CONFIG_DIR":       true,
		"ANTHROPIC_API_KEY":       true,
		"ANTHROPIC_AUTH_TOKEN":    true,
		"ANTHROPIC_BASE_URL":      true,
		"CLAUDE_CODE_OAUTH_TOKEN": true,
		"CLAUDE_CODE_USE_BEDROCK": true,
		"CLAUDE_CODE_USE_VERTEX":  true,
		"CLAUDE_CODE_USE_FOUNDRY": true,
		"ANTHROPIC_PROFILE":       true,
	}
	var environment []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !remove[strings.ToUpper(key)] {
			environment = append(environment, entry)
		}
	}
	if !profile.UsesDefaultConfig {
		environment = append(environment, "CLAUDE_CONFIG_DIR="+profile.ConfigDirectory)
	}
	return environment
}

func (store *Store) cliProfile(id string) (ClaudeProfile, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	index := store.claudeIndex(id)
	if index < 0 {
		return ClaudeProfile{}, errors.New("account not found")
	}
	profile := store.claude[index]
	if !profile.UsesDefaultConfig && store.isManaged(profile) {
		if err := prepareHistory(profile.ConfigDirectory, filepath.Join(store.home, ".claude")); err != nil {
			return ClaudeProfile{}, err
		}
	}
	return profile, nil
}

// LoginClaude 用现有 Claude 命令启动浏览器授权并等待登录完成
func (store *Store) LoginClaude(ctx context.Context, id string) error {
	profile, err := store.cliProfile(id)
	if err != nil {
		return err
	}
	executable, err := FindClaude(store.home)
	if err != nil {
		return err
	}
	store.mu.Lock()
	index := store.claudeIndex(id)
	if index < 0 {
		store.mu.Unlock()
		return errors.New("account not found")
	}
	if store.claude[index].IsLoggingIn {
		store.mu.Unlock()
		return errors.New("登录正在进行")
	}
	store.claude[index].IsLoggingIn = true
	store.mu.Unlock()
	store.notify()
	command := claudeCommand(ctx, executable, "auth", "login", "--claudeai")
	command.Dir = store.home
	command.Env = cliEnvironment(profile)
	showConsole(command)
	runError := command.Run()
	store.mu.Lock()
	index = store.claudeIndex(id)
	if index >= 0 {
		store.claude[index].IsLoggingIn = false
		store.reloadClaudeLocked(&store.claude[index])
		if runError == nil && store.claude[index].NeedsLogin {
			runError = errors.New("登录尚未完成，请重试")
		}
		if runError != nil {
			store.claude[index].Error = runError.Error()
		}
		if err := store.saveClaudeLocked(); runError == nil {
			runError = err
		}
	}
	store.mu.Unlock()
	store.notify()
	return runError
}

// StartClaude 用现有 Claude 命令和所选账号启动独立终端
func (store *Store) StartClaude(id, workingDir string) error {
	profile, err := store.cliProfile(id)
	if err != nil {
		return err
	}
	credential, err := store.readClaude(profile)
	if err != nil {
		return err
	}
	if credential == nil {
		return errors.New("请先登录此账号")
	}
	executable, err := FindClaude(store.home)
	if err != nil {
		return err
	}
	if workingDir == "" {
		workingDir = store.home
	}
	command := claudeCommand(context.Background(), executable)
	command.Dir = workingDir
	command.Env = cliEnvironment(profile)
	showConsole(command)
	if err := command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return nil
}

// SwitchClaude 将官方登录目录设为新终端的默认账号
func (store *Store) SwitchClaude(id string) error {
	profile, err := store.cliProfile(id)
	if err != nil {
		return err
	}
	credential, err := providers.ReadClaude(profile.ConfigDirectory, store.profilePath(profile))
	if err != nil {
		return err
	}
	if credential == nil {
		return errors.New("请先登录此账号")
	}
	directory := profile.ConfigDirectory
	if profile.UsesDefaultConfig {
		directory = ""
	}
	if err := setUserClaudeDirectory(directory); err != nil {
		return err
	}
	store.mu.Lock()
	for i := range store.claude {
		store.reloadClaudeLocked(&store.claude[i])
	}
	err = store.saveClaudeLocked()
	store.mu.Unlock()
	store.notify()
	return err
}
