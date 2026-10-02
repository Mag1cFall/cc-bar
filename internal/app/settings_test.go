package app

import (
	"encoding/json"
	"testing"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// TestSavedSettings 验证现有设置读取及新来源默认值
func TestSavedSettings(t *testing.T) {
	settings := model.DefaultSettings()
	data := []byte(`{"Providers":{"Codex":{"Enabled":true,"MenuBar":true},"Claude":{"Enabled":false}},"UsageVisibility":{"Pi":true},"LaunchAtLogin":true}`)
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if !settings.Providers[model.Codex].Enabled || settings.Providers[model.Claude].Enabled || !settings.UsageVisibility[model.UsagePi] || !settings.UsageVisibility[model.UsageOmp] {
		t.Fatalf("设置读取错误: %+v", settings)
	}
	copy := cloneSettings(settings)
	copy.UsageVisibility[model.UsagePi] = false
	if !settings.UsageVisibility[model.UsagePi] {
		t.Fatal("设置快照改变了原始值")
	}
}
