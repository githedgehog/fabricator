// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
	slogmulti "github.com/samber/slog-multi"
	"go.githedgehog.com/fabricator/pkg/fab/node"
	"go.githedgehog.com/fabricator/pkg/version"
	"gopkg.in/natefinch/lumberjack.v2"
)

func main() {
	if err := Run(context.Background()); err != nil {
		// TODO what if slog isn't initialized yet?
		slog.Error(err.Error())
		os.Exit(1)
	}
}

func Run(ctx context.Context) error {
	logLevel := slog.LevelDebug

	logW := os.Stderr
	handlers := []slog.Handler{
		tint.NewHandler(logW, &tint.Options{
			Level:      logLevel,
			TimeFormat: time.TimeOnly,
			NoColor:    !isatty.IsTerminal(logW.Fd()),
		}),
	}

	logFile := &lumberjack.Logger{
		Filename:   "/var/log/hhfab-node-config.log",
		MaxSize:    5, // MB
		MaxBackups: 4,
		MaxAge:     30, // days
		Compress:   true,
		FileMode:   0o644,
	}

	handlers = append(handlers, slog.NewTextHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	handler := slogmulti.Fanout(handlers...)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	args := []any{
		"version", version.Version,
	}

	if len(os.Args) != 3 {
		return fmt.Errorf("Usage: %s <workdir> <nodename>", os.Args[0]) //nolint:goerr113
	}

	workDir := os.Args[1]
	nodeName := os.Args[2]

	args = append(args, "workdir", workDir)
	args = append(args, "nodename", nodeName)

	slog.Info("Hedgehog Fabricator Node Config", args...)

	return node.DoConfig(ctx, workDir, nodeName) //nolint:wrapcheck
}
