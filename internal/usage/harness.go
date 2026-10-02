package usage

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mag1cFall/cc-bar/internal/model"
)

// SessionRoots 返回Pi和OMP的默认重定向与配置档会话目录
func SessionRoots(home string, app model.UsageApp) []string {
	config := ".pi"
	if app == model.UsageOmp {
		config = ".omp"
	}
	root := filepath.Join(home, config)
	roots := []string{filepath.Join(root, "agent", "sessions")}
	if app == model.UsageOmp {
		if override := os.Getenv("PI_CONFIG_DIR"); override != "" {
			root = filepath.Join(home, override)
			if filepath.IsAbs(override) {
				root = override
			}
			roots = append(roots, filepath.Join(root, "agent", "sessions"))
		}
		profiles, _ := os.ReadDir(filepath.Join(root, "profiles"))
		for _, profile := range profiles {
			if profile.IsDir() {
				roots = append(roots, filepath.Join(root, "profiles", profile.Name(), "agent", "sessions"))
			}
		}
	}
	if override := os.Getenv("PI_CODING_AGENT_DIR"); override != "" {
		if strings.HasPrefix(override, "~") {
			override = home + override[1:]
		}
		if absolute, err := filepath.Abs(override); err == nil {
			isPi := strings.Contains(strings.ToLower(absolute), ".pi")
			if (app == model.UsageOmp && !isPi) || (app == model.UsagePi && isPi) {
				roots = append(roots, filepath.Join(absolute, "sessions"))
			}
		}
	}
	unique := []string{}
	seen := map[string]bool{}
	for _, path := range roots {
		key := strings.ToLower(filepath.Clean(path))
		if !seen[key] {
			seen[key] = true
			unique = append(unique, path)
		}
	}
	return unique
}

// harnessHeader 读取固定标题槽和会话头且保留大日志的增量扫描
func harnessHeader(path string) (id, title, cwd string) {
	input, err := os.Open(path)
	if err != nil {
		return "", "", ""
	}
	defer input.Close()
	reader := bufio.NewReaderSize(input, 4096)
	for index := 0; index < 4; index++ {
		line, err := reader.ReadBytes('\n')
		root, parseErr := parse(line)
		if parseErr == nil {
			if str(root, "type") == "title" {
				title = cleanTitle(str(root, "title"))
			}
			if str(root, "type") == "session" {
				return str(root, "id"), coalesce(title, cleanTitle(str(root, "title"))), str(root, "cwd")
			}
		}
		if err != nil {
			break
		}
	}
	return "", title, ""
}

// ompParent 查找子代理和顾问日志所属的主会话
func ompParent(path string, roots []string, headers map[string]string) string {
	for _, root := range roots {
		if !within(root, path) {
			continue
		}
		for directory := filepath.Dir(path); within(root, directory) && directory != root; directory = filepath.Dir(directory) {
			parentFile := directory + ".jsonl"
			if id, known := headers[parentFile]; known {
				if id != "" {
					return id
				}
				continue
			}
			id, _, _ := harnessHeader(parentFile)
			headers[parentFile] = id
			if id != "" {
				return id
			}
		}
	}
	return ""
}
