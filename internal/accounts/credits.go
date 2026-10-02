package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mag1cFall/cc-bar/internal/providers"
)

type resetAttempt struct {
	CreditID  string `json:"creditId"`
	RequestID string `json:"requestId"`
}

// readResetAttempts 保留网络中断时的同一次兑换标识
func (store *Store) readResetAttempts() (map[string]resetAttempt, error) {
	result := map[string]resetAttempt{}
	data, err := os.ReadFile(filepath.Join(store.dataDir, "reset-attempts.json"))
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal(data, &result)
	return result, err
}

func creditDate(value any) *time.Time {
	if text, ok := value.(string); ok {
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return &parsed
		}
	}
	if number, ok := value.(float64); ok && number > 0 {
		if number > 1e10 {
			number /= 1000
		}
		parsed := time.Unix(0, int64(number*1e9))
		return &parsed
	}
	return nil
}
func parseCredits(root map[string]any) *ResetCredits {
	result := &ResetCredits{Credits: []ResetCredit{}}
	if count, ok := root["available_count"].(float64); ok {
		result.Available = int(count)
	}
	if rows, ok := root["credits"].([]any); ok {
		for _, value := range rows {
			row := objectValue(value)
			id := text(row, "id")
			if id == "" {
				id = text(row, "credit_id")
			}
			result.Credits = append(result.Credits, ResetCredit{
				ID:        id,
				Title:     text(row, "title"),
				Status:    text(row, "status"),
				GrantedAt: creditDate(row["granted_at"]),
				ExpiresAt: creditDate(row["expires_at"]),
			})
		}
	}
	return result
}

// FetchResetCredits 读取所选账号现有次数
func (store *Store) FetchResetCredits(ctx context.Context, id string) (*ResetCredits, error) {
	store.refreshMu.Lock()
	defer store.refreshMu.Unlock()
	credential, err := store.codexCredential(ctx, id)
	if err != nil {
		return nil, err
	}
	root, err := providers.FetchCredits(ctx, credential)
	if err != nil {
		return nil, err
	}
	return parseCredits(root), nil
}

// ConsumeResetCredit 仅在本次用户确认后消费一份指定次数
func (store *Store) ConsumeResetCredit(ctx context.Context, id, creditID string, confirmed bool, idempotencyKey string) (*ResetResult, error) {
	if !confirmed {
		return nil, errors.New("请确认使用一次额度重置")
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, errors.New("兑换请求缺少幂等标识")
	}
	store.refreshMu.Lock()
	defer store.refreshMu.Unlock()
	credential, err := store.codexCredential(ctx, id)
	if err != nil {
		return nil, err
	}
	attempts, err := store.readResetAttempts()
	if err != nil {
		return nil, err
	}
	accountKey := credential.AccountID + ":" + credential.UserID
	previous, retrying := attempts[accountKey]
	if retrying {
		if previous.CreditID != creditID {
			return nil, errors.New("前次兑换尚在确认，请先重试相同的重置次数")
		}
		idempotencyKey = previous.RequestID
	}
	snapshot, _, err := providers.FetchCodexIdentity(ctx, credential)
	if err != nil {
		return nil, err
	}
	if id != "" && id != "current" {
		store.mu.Lock()
		if index := store.codexIndex(id); index >= 0 {
			store.codex[index].Snapshot = snapshot
		}
		store.mu.Unlock()
	}
	root, err := providers.FetchCredits(ctx, credential)
	if err != nil {
		return nil, err
	}
	credits := parseCredits(root)
	if credits.Available <= 0 && !retrying {
		return &ResetResult{Code: "noCredit", Credits: credits}, nil
	}
	if creditID != "" && !retrying {
		found := false
		for _, credit := range credits.Credits {
			if credit.ID == creditID {
				if credit.ExpiresAt != nil && !credit.ExpiresAt.After(time.Now()) {
					return nil, errors.New("所选重置次数已过期")
				}
				status := strings.ToLower(credit.Status)
				if status != "available" && status != "active" && status != "" {
					return nil, fmt.Errorf("所选重置次数状态为 %s", credit.Status)
				}
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("所选重置次数已变更，请刷新")
		}
	}
	attempts[accountKey] = resetAttempt{CreditID: creditID, RequestID: idempotencyKey}
	attemptPath := filepath.Join(store.dataDir, "reset-attempts.json")
	if err := providers.WriteJSON(attemptPath, attempts); err != nil {
		return nil, err
	}
	response, err := providers.ConsumeCredit(ctx, credential, creditID, idempotencyKey)
	if err != nil {
		return nil, err
	}
	result := &ResetResult{Code: text(response, "code")}
	if count, ok := response["windows_reset"].(float64); ok {
		result.WindowsReset = int(count)
	}
	if result.Code == "" {
		result.Code = text(response, "status")
	}
	if result.Code != "" {
		delete(attempts, accountKey)
		if err := providers.WriteJSON(attemptPath, attempts); err != nil {
			return result, err
		}
	}
	if result.Code == "reset" || result.Code == "alreadyRedeemed" || result.Code == "already_redeemed" {
		if current, _, err := providers.FetchCodexIdentity(ctx, credential); err == nil && id != "" && id != "current" {
			store.mu.Lock()
			if index := store.codexIndex(id); index >= 0 {
				store.codex[index].Snapshot = current
			}
			store.mu.Unlock()
		}
		if fresh, err := providers.FetchCredits(ctx, credential); err == nil {
			result.Credits = parseCredits(fresh)
		}
	}
	store.notify()
	return result, nil
}
