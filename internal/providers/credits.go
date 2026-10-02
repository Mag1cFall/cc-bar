package providers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// FetchCredits 读取官方可用额度重置次数
func FetchCredits(ctx context.Context, credential *model.Credential) (map[string]any, error) {
	return requestJSON(ctx, http.MethodGet, "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits", credential, nil, codexHeaders(credential))
}

// ConsumeCredit 使用官方幂等请求兑换指定重置次数
func ConsumeCredit(ctx context.Context, credential *model.Credential, creditID, idempotencyKey string) (map[string]any, error) {
	body := map[string]string{"redeem_request_id": idempotencyKey}
	if creditID != "" {
		body["credit_id"] = creditID
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return requestJSON(ctx, http.MethodPost, "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume", credential, data, codexHeaders(credential))
}
