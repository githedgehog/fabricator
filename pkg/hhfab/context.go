package hhfab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

var (
	ErrContextAlreadyExists = fmt.Errorf("already exists")
	ErrContextNotFound      = fmt.Errorf("not found")
	ErrInvalidContextName   = fmt.Errorf("invalid")
)

var ContextNameRegex = regexp.MustCompile(`^[a-z][a-z0-9-]{1,10}[a-z0-9]$`)

func IsContextNameValid(name string) bool {
	return ContextNameRegex.MatchString(name)
}

type ContextCreateConfig struct {
	RegistryConfig
}

func (cfg *Config) ContextCreate(_ context.Context, name string, create ContextCreateConfig) error {
	if !IsContextNameValid(name) {
		return fmt.Errorf("context name %q: %w", name, ErrInvalidContextName)
	}

	contextDir := filepath.Join(cfg.BaseDir, name)
	_, err := os.Stat(contextDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking context dir: %w", err)
	}
	if err == nil {
		return fmt.Errorf("context %q: %w", name, ErrContextAlreadyExists)
	}

	if err := os.MkdirAll(contextDir, 0o700); err != nil {
		return fmt.Errorf("creating context dir: %w", err)
	}

	ctxCfg := ContextConfig{
		Registry: create.RegistryConfig,
	}
	ctxCfgData, err := yaml.Marshal(ctxCfg)
	if err != nil {
		return fmt.Errorf("marshalling context: %w", err)
	}

	contextFile := filepath.Join(contextDir, ContextConfigFile)
	if err := os.WriteFile(contextFile, ctxCfgData, 0o600); err != nil {
		return fmt.Errorf("writing context config: %w", err)
	}

	args := []any{}
	if ctxCfg.Registry.Repo != DefaultRepo {
		args = append(args, "registry-repo", ctxCfg.Registry.Repo)
	}
	if ctxCfg.Registry.Prefix != DefaultPrefix {
		args = append(args, "registry-prefix", ctxCfg.Registry.Prefix)
	}
	if ctxCfg.Dev {
		args = append(args, "dev")
	}
	if ctxCfg.VLAB {
		args = append(args, "vlab")
	}

	slog.Info("Context created: "+name, args...)

	return nil
}

func (cfg *Config) ContextDelete(_ context.Context, name string) error {
	if !IsContextNameValid(name) {
		return fmt.Errorf("context name %q: %w", name, ErrInvalidContextName)
	}

	contextDir := filepath.Join(cfg.BaseDir, name)
	_, err := os.Stat(contextDir)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("context %s: %w", name, ErrContextNotFound)
	}
	if err != nil {
		return fmt.Errorf("checking context dir: %w", err)
	}

	if err := os.RemoveAll(contextDir); err != nil {
		return fmt.Errorf("removing context dir: %w", err)
	}

	return nil
}

func (cfg *Config) ContextList(_ context.Context) error {
	files, err := os.ReadDir(cfg.BaseDir)
	if err != nil {
		return fmt.Errorf("reading base dir: %w", err)
	}

	var contexts []string
	for _, file := range files {
		if !file.IsDir() {
			continue
		}

		if !IsContextNameValid(file.Name()) {
			continue
		}

		if _, err := os.Stat(filepath.Join(cfg.BaseDir, file.Name(), ContextConfigFile)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}

			return fmt.Errorf("checking context: %w", err)
		}

		contexts = append(contexts, file.Name())
	}

	slices.Sort(contexts)

	if len(contexts) == 0 {
		slog.Info("No contexts found")

		return nil
	}

	currentContext, err := GetCurrentContext(cfg.BaseDir)
	if err != nil {
		return err
	}

	slog.Info("Contexts:")
	for _, context := range contexts {
		if context == currentContext {
			slog.Info("  * " + context)
		} else {
			slog.Info("    " + context)
		}
	}

	return nil
}

func GetCurrentContext(baseDir string) (string, error) {
	currentContext := DefaultContext

	currCtxData, err := os.ReadFile(filepath.Join(baseDir, CurrentContextFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("reading current context: %w", err)
	}
	if err == nil {
		currentContext = strings.TrimSpace(string(currCtxData))
	}

	return currentContext, nil
}

func (cfg *Config) ContextUse(_ context.Context, name string) error {
	err := os.WriteFile(filepath.Join(cfg.BaseDir, CurrentContextFile), []byte(name), 0o600)
	if err != nil {
		return fmt.Errorf("writing current context: %w", err)
	}

	return nil
}
