package app

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

func readSettings(directory string) (model.Settings, error) {
	settings := model.DefaultSettings()
	contents, err := os.ReadFile(filepath.Join(directory, "settings.json"))
	if os.IsNotExist(err) {
		return settings, nil
	}
	if err != nil {
		return settings, err
	}
	err = json.Unmarshal(contents, &settings)
	return settings, err
}

func writeJSON(directory, name string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(directory, name)
	if err = os.WriteFile(path+".tmp", contents, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func cloneSettings(settings model.Settings) model.Settings {
	providers := make(map[model.QuotaApp]model.ProviderDisplaySettings, len(settings.Providers))
	for app, value := range settings.Providers {
		providers[app] = value
	}
	visibility := make(map[model.UsageApp]bool, len(settings.UsageVisibility))
	for app, value := range settings.UsageVisibility {
		visibility[app] = value
	}
	settings.Providers, settings.UsageVisibility = providers, visibility
	return settings
}
