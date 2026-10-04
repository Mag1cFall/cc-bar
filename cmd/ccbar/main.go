package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/Mag1cFall/cc-bar/internal/app"
	"github.com/Mag1cFall/cc-bar/internal/desktop"
	"gopkg.in/natefinch/lumberjack.v2"
)

var version = "0.1.1"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	handled, err := app.HandleUpdateArguments(os.Args[1:])
	if handled || err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	directory := filepath.Join(os.Getenv("LOCALAPPDATA"), "CCBar")
	if os.Getenv("CCBAR_DATA_DIR") != "" {
		directory = os.Getenv("CCBAR_DATA_DIR")
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
	service, err := app.New(directory, home, version, logger)
	if err != nil {
		return err
	}
	service.SetLogLevel(&logLevel)
	return desktop.Run(service, logger)
}
