package app

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var emailPattern = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)
var tokenPattern = regexp.MustCompile(`(?i)(bearer\s+|(?:access_token|refresh_token|token|api_key)["\s:=]+)[a-zA-Z0-9._\-]+`)

func (service *Service) redactText(text string) string {
	encodedHome, _ := json.Marshal(service.home)
	text = strings.ReplaceAll(text, strings.Trim(string(encodedHome), `"`), "%USERPROFILE%")
	text = strings.ReplaceAll(text, service.home, "%USERPROFILE%")
	text = emailPattern.ReplaceAllString(text, "[account]")
	return tokenPattern.ReplaceAllString(text, "${1}[credential]")
}

// ExportDiagnostics 导出设置运行状态与去除身份信息的近期日志
func (service *Service) ExportDiagnostics() (string, error) {
	path := filepath.Join(service.dataDir, "ccbar-diagnostics.zip")
	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	archive := zip.NewWriter(file)
	writeEntry := func(name string, data []byte) error {
		entry, createErr := archive.Create(name)
		if createErr != nil {
			return createErr
		}
		_, writeErr := entry.Write([]byte(service.redactText(string(data))))
		return writeErr
	}
	snapshot := service.GetSnapshot()
	for index := range snapshot.Providers {
		snapshot.Providers[index].Account = nil
	}
	snapshot.ClaudeAccounts = nil
	snapshot.ClaudeLogin = nil
	snapshot.ImportedCodexAccounts = nil
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err == nil {
		err = writeEntry("status.json", data)
	}
	entries, readErr := os.ReadDir(filepath.Join(service.dataDir, "Logs"))
	if err == nil && readErr == nil {
		start := max(0, len(entries)-5)
		for _, entry := range entries[start:] {
			if entry.IsDir() {
				continue
			}
			contents, logErr := os.ReadFile(filepath.Join(service.dataDir, "Logs", entry.Name()))
			if logErr != nil {
				continue
			}
			if len(contents) > 2<<20 {
				contents = contents[len(contents)-(2<<20):]
			}
			if err = writeEntry("Logs/"+entry.Name(), contents); err != nil {
				break
			}
		}
	}
	closeErr := archive.Close()
	fileErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if fileErr != nil {
		return "", fileErr
	}
	return path, nil
}
