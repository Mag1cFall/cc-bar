package providers

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type object = map[string]any

// DecodeObject 读取服务响应的 JSON 对象
func DecodeObject(data []byte) (map[string]any, error) {
	var result object
	err := json.Unmarshal(data, &result)
	return result, err
}

// obj 读取可选 JSON 对象
func obj(value any) object {
	result, _ := value.(map[string]any)
	return result
}

// rows 读取可选 JSON 数组
func rows(value any) []any {
	result, _ := value.([]any)
	return result
}

// str 按字段优先级读取非空文本
func str(root object, keys ...string) string {
	for _, key := range keys {
		if text, ok := root[key].(string); ok && text != "" {
			return text
		}
	}
	return ""
}

// num 同时支持数字和数字字符串
func num(root object, key string) *float64 {
	var result float64
	switch value := root[key].(type) {
	case float64:
		result = value
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil
		}
		result = parsed
	default:
		return nil
	}
	if math.IsInf(result, 0) || math.IsNaN(result) {
		return nil
	}
	return &result
}

// val 将缺失数值视为零
func val(root object, key string) float64 {
	if value := num(root, key); value != nil {
		return *value
	}
	return 0
}

// date 支持 ISO 时间和秒或毫秒时间戳
func date(value any) *time.Time {
	if text, ok := value.(string); ok {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05Z07:00"} {
			if parsed, err := time.Parse(layout, text); err == nil {
				return &parsed
			}
		}
	}
	valueNumber := num(object{"value": value}, "value")
	if valueNumber == nil || *valueNumber <= 0 {
		return nil
	}
	number := *valueNumber
	if number > 1e10 {
		number /= 1000
	}
	parsed := time.Unix(0, int64(number*1e9))
	return &parsed
}
func integer(value int) *int {
	return &value
}

func boolean(root object, key string) *bool {
	value, ok := root[key].(bool)
	if !ok {
		return nil
	}
	return &value
}

// readObject 从本机文件读取 JSON 对象
func readObject(path string) (object, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DecodeObject(data)
}

// WriteJSON 原子保存凭据与账号元数据
func WriteJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WriteBytes(path, data)
}

// WriteBytes 原子替换目标文件
func WriteBytes(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".ccbar-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeError := file.Close()
	if err != nil {
		return err
	}
	if closeError != nil {
		return closeError
	}
	return os.Rename(temporary, path)
}

// Claims 提取 JWT 展示身份
func Claims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return object{}
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return object{}
	}
	result, _ := DecodeObject(data)
	return result
}

// Claim 读取普通或 OpenAI 命名空间身份字段
func Claim(token, key string) string {
	claims := Claims(token)
	if value := str(claims, key); value != "" {
		return value
	}
	return str(obj(claims["https://api.openai.com/auth"]), key)
}

// TokenExpiry 读取 JWT 过期时间
func TokenExpiry(token string) *time.Time {
	return date(Claims(token)["exp"])
}
