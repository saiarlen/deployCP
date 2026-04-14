package bootstrap

import (
	"log/slog"
	"os"

	"deploycp/internal/config"
)

func NewLogger(cfg *config.Config) *slog.Logger {
	level := slog.LevelInfo
	if cfg.App.Env != "production" {
		level = slog.LevelDebug
	}
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(h)
}
