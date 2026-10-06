package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Mag1cFall/cc-bar/internal/app"
	"github.com/Mag1cFall/cc-bar/internal/browser"
	"github.com/Mag1cFall/cc-bar/internal/desktop"
	"gopkg.in/natefinch/lumberjack.v2"
)

var version = "0.1.4"

func main() {
	if err := run(); err != nil {
		desktop.ReportStartupError(err)
		os.Exit(1)
	}
}

func run() (err error) {
	handled, err := app.HandleUpdateArguments(os.Args[1:])
	if handled || err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	directory := os.Getenv("CCBAR_DATA_DIR")
	if directory == "" {
		cache, cacheErr := os.UserCacheDir()
		if cacheErr != nil {
			return cacheErr
		}
		directory = filepath.Join(cache, "CCBar")
	}
	if err = os.MkdirAll(filepath.Join(directory, "Logs"), 0700); err != nil {
		return err
	}
	// 运行日志最多保留当前文件与两份备份
	logFile := &lumberjack.Logger{
		Filename:   filepath.Join(directory, "Logs", "ccbar.log"),
		MaxSize:    5,
		MaxBackups: 2,
		MaxAge:     14,
	}
	defer logFile.Close()
	var logLevel slog.LevelVar
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{Level: &logLevel}))
	defer func() {
		if cause := recover(); cause != nil {
			err = fmt.Errorf("启动异常: %v", cause)
		}
		if err != nil {
			logger.Error("启动失败", "error", err)
		}
	}()
	closeProgress := func() {}
	if !browser.Ready(directory) {
		closeProgress = desktop.ShowStartupProgress()
		defer closeProgress()
	}
	browserPath, err := browser.Prepare(directory)
	if err != nil {
		return err
	}
	service, err := app.New(directory, home, version, logger)
	if err != nil {
		return err
	}
	service.SetLogLevel(&logLevel)
	return desktop.Run(service, logger, browserPath, closeProgress)
}
