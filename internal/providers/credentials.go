package providers

import (
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Mag1cFall/cc-bar/internal/model"
	"github.com/Mag1cFall/cc-bar/internal/secrets"
	_ "modernc.org/sqlite"
)

// Discover 发现已安装客户端的本机登录
func Discover(app model.QuotaApp, settings model.Settings) (*model.Credential, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	switch app {
	case model.Codex:
		directory := os.Getenv("CODEX_HOME")
		if directory == "" {
			directory = filepath.Join(home, ".codex")
		}
		return ReadCodex(filepath.Join(directory, "auth.json"))
	case model.Claude:
		directory, profile := ClaudePaths(home)
		cli, cliError := ReadClaude(directory, profile)
		if cli != nil && (cli.ExpiresAt == nil || cli.ExpiresAt.After(time.Now().Add(30*time.Second)) || cli.AccountUUID == "" || cli.OrganizationUUID == "") {
			return cli, nil
		}
		desktop, _ := ReadClaudeDesktop(cli)
		if desktop != nil {
			return desktop, nil
		}
		return cli, cliError
	case model.Cursor:
		return ReadCursor(filepath.Join(os.Getenv("APPDATA"), "Cursor", "User", "globalStorage", "state.vscdb"))
	case model.Antigravity:
		return ReadAntigravity(home)
	case model.CommandCode:
		return ReadCommandCode(home, filepath.Join(os.Getenv("LOCALAPPDATA"), "CCBar"), settings.CommandCodeCredentialPreference)
	default:
		return nil, errors.New("unknown provider")
	}
}

// ClaudePaths 返回官方当前登录与身份文件路径
func ClaudePaths(home string) (string, string) {
	if directory := os.Getenv("CLAUDE_CONFIG_DIR"); directory != "" {
		absolute, err := filepath.Abs(directory)
		if err == nil {
			directory = absolute
		}
		return directory, filepath.Join(directory, ".claude.json")
	}
	return filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")
}

// ReadCodex 读取 OAuth 或 PAT 身份
func ReadCodex(path string) (*model.Credential, error) {
	root, err := readObject(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	tokens := obj(root["tokens"])
	access := str(tokens, "access_token")
	personal := access == ""
	if personal {
		access = str(root, "personal_access_token")
	}
	if access == "" {
		return nil, nil
	}
	idToken := str(tokens, "id_token")
	accountID := str(tokens, "account_id")
	if accountID == "" {
		accountID = Claim(access, "chatgpt_account_id")
	}
	if accountID == "" {
		accountID = Claim(idToken, "chatgpt_account_id")
	}
	userID := Claim(access, "chatgpt_user_id")
	if userID == "" {
		userID = Claim(idToken, "chatgpt_user_id")
	}
	plan := Claim(idToken, "chatgpt_plan_type")
	if plan == "" {
		plan = Claim(access, "chatgpt_plan_type")
	}
	return &model.Credential{
		AccessToken:           access,
		RefreshToken:          str(tokens, "refresh_token"),
		Email:                 Claim(idToken, "email"),
		AccountID:             accountID,
		UserID:                userID,
		SubscriptionType:      plan,
		IsPersonalAccessToken: personal,
		ExpiresAt:             TokenExpiry(access),
		LastRefresh:           date(root["last_refresh"]),
		Source:                path,
	}, nil
}

// ReadClaude 读取独立登录目录及对应身份文件
func ReadClaude(directory, profilePath string) (*model.Credential, error) {
	path := filepath.Join(directory, ".credentials.json")
	root, err := readObject(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	oauth := obj(root["claudeAiOauth"])
	access := str(oauth, "accessToken", "access_token")
	if access == "" {
		return nil, nil
	}
	credential := &model.Credential{
		AccessToken:      access,
		RefreshToken:     str(oauth, "refreshToken", "refresh_token"),
		Scopes:           stringsList(oauth["scopes"]),
		RateLimitTier:    str(oauth, "rateLimitTier"),
		Email:            str(oauth, "emailAddress", "email"),
		SubscriptionType: str(oauth, "subscriptionType"),
		ExpiresAt:        date(oauth["expiresAt"]),
		Source:           path,
	}
	if profilePath == "" {
		profilePath = filepath.Join(directory, ".claude.json")
	}
	profile, profileError := readObject(profilePath)
	if profileError == nil {
		identity := obj(profile["oauthAccount"])
		if credential.Email == "" {
			credential.Email = str(identity, "emailAddress")
		}
		credential.AccountUUID = str(identity, "accountUuid")
		credential.OrganizationUUID = str(identity, "organizationUuid")
	}
	return credential, nil
}

// ReadClaudeDesktop 解密当前用户的官方桌面令牌缓存
func ReadClaudeDesktop(cli *model.Credential) (*model.Credential, error) {
	directories := []string{filepath.Join(os.Getenv("APPDATA"), "Claude")}
	packages, _ := filepath.Glob(filepath.Join(os.Getenv("LOCALAPPDATA"), "Packages", "Claude_*"))
	for _, item := range packages {
		directories = append(directories, filepath.Join(item, "LocalCache", "Roaming", "Claude"))
	}
	for _, directory := range directories {
		value, err := ReadClaudeDesktopDirectory(directory, cli)
		if err == nil && value != nil {
			return value, nil
		}
	}
	return nil, nil
}

// ReadClaudeDesktopDirectory 读取指定 Desktop 会话目录的官方 OAuth 缓存
func ReadClaudeDesktopDirectory(directory string, cli *model.Credential) (*model.Credential, error) {
	state, err := readObject(filepath.Join(directory, "Local State"))
	if err != nil {
		return nil, err
	}
	wrapped, err := base64.StdEncoding.DecodeString(str(obj(state["os_crypt"]), "encrypted_key"))
	if err != nil {
		return nil, err
	}
	if len(wrapped) < 5 || string(wrapped[:5]) != "DPAPI" {
		return nil, nil
	}
	key, err := secrets.UnprotectBytes(wrapped[5:])
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	config, err := readObject(filepath.Join(directory, "config.json"))
	if err != nil {
		return nil, err
	}
	preferred := str(config, "lastKnownAccountUuid")
	var entries []*model.Credential
	for _, name := range []string{"oauth:tokenCacheV2", "oauth:tokenCache"} {
		encoded := str(config, name)
		if encoded == "" {
			continue
		}
		buffer, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(buffer) < 31 || string(buffer[:3]) != "v10" {
			continue
		}
		plain, err := gcm.Open(nil, buffer[3:15], buffer[15:], nil)
		if err != nil {
			continue
		}
		cache, err := DecodeObject(plain)
		if err != nil {
			continue
		}
		for cacheKey, value := range cache {
			if !strings.HasPrefix(cacheKey, "acct:") || !strings.Contains(cacheKey, "user:profile") {
				continue
			}
			pieces := strings.SplitN(cacheKey[5:], "|", 2)
			if len(pieces) != 2 {
				continue
			}
			tail := strings.SplitN(pieces[1], ":", 3)
			if len(tail) < 3 {
				continue
			}
			entry := obj(value)
			access := str(entry, "token")
			expiry := date(entry["expiresAt"])
			if access == "" || expiry == nil || expiry.Before(time.Now().Add(5*time.Second)) {
				continue
			}
			if cli != nil && cli.AccountUUID != "" && (cli.OrganizationUUID == "" || !strings.EqualFold(pieces[0], cli.AccountUUID) || !strings.EqualFold(tail[1], cli.OrganizationUUID)) {
				continue
			}
			source := "Claude Desktop"
			if tail[0] == "9d1c250a-e61b-44d9-88ed-5944d1962f5e" {
				source = "Claude Desktop Code"
			}
			credential := &model.Credential{
				AccessToken:      access,
				RefreshToken:     str(entry, "refreshToken", "refresh_token"),
				Scopes:           desktopScopes(tail[2]),
				RateLimitTier:    str(entry, "rateLimitTier"),
				AccountUUID:      pieces[0],
				OrganizationUUID: tail[1],
				SubscriptionType: str(entry, "subscriptionType"),
				ExpiresAt:        expiry,
				Source:           source,
			}
			if cli != nil {
				credential.Email = cli.Email
				if credential.SubscriptionType == "" {
					credential.SubscriptionType = cli.SubscriptionType
				}
			}
			entries = append(entries, credential)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if cli == nil && preferred != "" {
			a, b := strings.EqualFold(entries[i].AccountUUID, preferred), strings.EqualFold(entries[j].AccountUUID, preferred)
			if a != b {
				return a
			}
		}
		if entries[i].Source != entries[j].Source {
			return entries[i].Source == "Claude Desktop Code"
		}
		return entries[i].ExpiresAt.After(*entries[j].ExpiresAt)
	})
	if len(entries) > 0 {
		home, _ := os.UserHomeDir()
		if profile, err := readObject(filepath.Join(home, ".claude.json")); err == nil {
			identity := obj(profile["oauthAccount"])
			if strings.EqualFold(str(identity, "accountUuid"), entries[0].AccountUUID) {
				if entries[0].Email == "" {
					entries[0].Email = str(identity, "emailAddress")
				}
			}
		}
		return entries[0], nil
	}
	return nil, nil
}

func desktopScopes(value string) []string {
	if start := strings.Index(value, "user:"); start >= 0 {
		value = value[start:]
	}
	return strings.Fields(value)
}

func stringsList(value any) []string {
	result := []string{}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
	}
	return result
}

// ReadCursor 从官方只读 SQLite 登录状态读取凭据
func ReadCursor(path string) (*model.Credential, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer database.Close()
	var token string
	err = database.QueryRow("SELECT value FROM ItemTable WHERE key = 'cursorAuth/accessToken' LIMIT 1").Scan(&token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	token = strings.TrimSpace(token)
	claims := Claims(token)
	sub := str(claims, "sub")
	segments := strings.Split(sub, "|")
	userID := segments[len(segments)-1]
	expiry := date(claims["exp"])
	if userID == "" || expiry == nil || expiry.Before(time.Now().Add(time.Minute)) {
		return nil, nil
	}
	for _, r := range userID {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._-", r) {
			return nil, nil
		}
	}
	return &model.Credential{AccessToken: token, Source: "Cursor", AccountID: userID, Email: str(claims, "email"), ExpiresAt: expiry}, nil
}

// ReadAntigravity 沿用官方客户端的令牌来源顺序
func ReadAntigravity(home string) (*model.Credential, error) {
	for _, name := range []string{"jetski-standalone-oauth-token", "oauth_creds.json"} {
		path := filepath.Join(home, ".gemini", name)
		root, err := readObject(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		body := root
		if nested := obj(root["token"]); name == "jetski-standalone-oauth-token" && nested != nil {
			body = nested
		}
		access, refresh := str(body, "access_token", "accessToken"), str(body, "refresh_token", "refreshToken")
		if access == "" && refresh == "" {
			continue
		}
		expiry := date(body["expiry_date"])
		if value := date(body["expiry"]); value != nil {
			expiry = value
		}
		email := str(root, "email")
		if email == "" {
			email = Claim(str(root, "id_token"), "email")
		}
		return &model.Credential{AccessToken: access, RefreshToken: refresh, Email: email, ExpiresAt: expiry, Source: path}, nil
	}
	return nil, nil
}

func sanitizeToken(token string) string {
	token = strings.TrimSpace(token)
	if len(token) < 10 {
		return ""
	}
	for _, r := range token {
		if r < 0x20 || r > 0x7e {
			return ""
		}
	}
	return token
}

// ReadCommandCode 支持自动来源和原有手动 DPAPI 密钥
func ReadCommandCode(home, dataDir, preference string) (*model.Credential, error) {
	manual := func() (*model.Credential, error) {
		data, err := os.ReadFile(filepath.Join(dataDir, "commandcode-key"))
		if os.IsNotExist(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		token, err := secrets.Unprotect(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, err
		}
		if token = sanitizeToken(token); token != "" {
			return &model.Credential{AccessToken: token, Source: "Manual API Key"}, nil
		}
		return nil, nil
	}
	if preference == "manual" {
		return manual()
	}
	candidates := []string{
		filepath.Join(home, ".commandcode", "auth.json"),
		filepath.Join(home, ".pi", "agent", "auth.json"),
		filepath.Join(home, ".local", "share", "opencode", "auth.json"),
	}
	for i, path := range candidates {
		root, err := readObject(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		token := ""
		if i == 0 {
			token = str(root, "access", "access_token", "apiKey", "token")
		} else {
			for _, key := range []string{"commandcode", "command-code"} {
				if value, ok := root[key].(string); ok {
					token = value
				} else {
					token = str(obj(root[key]), "access", "access_token", "key", "apiKey", "token")
				}
				if token != "" {
					break
				}
			}
		}
		if token = sanitizeToken(token); token != "" {
			return &model.Credential{AccessToken: token, Source: path}, nil
		}
	}
	for _, key := range []string{"COMMAND_CODE_API_KEY", "COMMANDCODE_API_KEY"} {
		if token := sanitizeToken(os.Getenv(key)); token != "" {
			return &model.Credential{AccessToken: token, Source: "ENV"}, nil
		}
	}
	return manual()
}

// SaveCommandCodeKey 保存当前用户手动密钥
func SaveCommandCodeKey(dataDir, token string) error {
	token = sanitizeToken(token)
	if token == "" {
		return errors.New("API key must contain at least 10 printable ASCII characters")
	}
	protected, err := secrets.Protect(token)
	if err != nil {
		return err
	}
	return WriteBytes(filepath.Join(dataDir, "commandcode-key"), []byte(protected))
}

// DeleteCommandCodeKey 删除手动密钥
func DeleteCommandCodeKey(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, "commandcode-key"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
