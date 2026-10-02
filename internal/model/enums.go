package model

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// UnmarshalJSON 读取服务编号和设置中的服务名称
func (app *QuotaApp) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		return app.UnmarshalText([]byte(text))
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*app = QuotaApp(value)
	return nil
}

// UnmarshalJSON 读取用量来源编号和设置中的来源名称
func (app *UsageApp) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		return app.UnmarshalText([]byte(text))
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*app = UsageApp(value)
	return nil
}

// UnmarshalText 读取设置中持久化的服务名称或枚举值
func (app *QuotaApp) UnmarshalText(text []byte) error {
	value, err := enumValue(string(text), []string{"Codex", "Claude", "Antigravity", "Cursor", "CommandCode"})
	if err == nil {
		*app = QuotaApp(value)
	}
	return err
}

// UnmarshalText 读取设置中持久化的用量来源名称或枚举值
func (app *UsageApp) UnmarshalText(text []byte) error {
	value, err := enumValue(string(text), []string{"Codex", "Claude", "Cursor", "Pi", "Opencode", "Dsh", "Omp"})
	if err == nil {
		*app = UsageApp(value)
	}
	return err
}

func enumValue(text string, names []string) (int, error) {
	for index, name := range names {
		if strings.EqualFold(text, name) {
			return index, nil
		}
	}
	value, err := strconv.Atoi(text)
	if err == nil && value >= 0 && value < len(names) {
		return value, nil
	}
	return 0, fmt.Errorf("未知服务 %q", text)
}
