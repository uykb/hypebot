package logger

import (
	"log/slog"
	"os"
)

var log *slog.Logger

func init() {
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	handler := slog.NewJSONHandler(os.Stdout, opts)
	log = slog.New(handler)
}

func Info(msg string, args ...interface{}) {
	log.Info(msg, args...)
}

func Error(msg string, err error, args ...interface{}) {
	allArgs := append([]interface{}{"error", err}, args...)
	log.Error(msg, allArgs...)
}

func Fatal(msg string, err error) {
	log.Error(msg, "error", err)
	os.Exit(1)
}

func Debug(msg string, args ...interface{}) {
	log.Debug(msg, args...)
}

func Warn(msg string, args ...interface{}) {
	log.Warn(msg, args...)
}
