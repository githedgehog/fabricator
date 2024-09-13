package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"time"

	dockertypes "github.com/containers/image/v5/types"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
	"sigs.k8s.io/yaml"
)

type RegistryCredentialsStore map[string]RegistryCredentials // e.g. ghcr.io -> {username, password}

type RegistryCredentials struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type credsFileStore struct {
	Credentials RegistryCredentialsStore `json:"credentials,omitempty"`
}

func (s RegistryCredentialsStore) Load(file string) error {
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return nil
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("reading credentials file: %w", err)
	}

	creds := credsFileStore{}
	if err := yaml.UnmarshalStrict(data, &creds); err != nil {
		return fmt.Errorf("unmarshalling credentials: %w", err)
	}

	maps.Copy(s, creds.Credentials)

	return nil
}

func (s RegistryCredentialsStore) Save(file string) error {
	creds := credsFileStore{Credentials: s}
	data, err := yaml.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshalling credentials: %w", err)
	}

	if err := os.WriteFile(file, data, 0o600); err != nil {
		return fmt.Errorf("writing credentials file: %w", err)
	}

	return nil
}

func (s RegistryCredentialsStore) GetORASCredsFor(registry string) orasauth.CredentialFunc {
	if registry == "" {
		return nil
	}

	if creds, exists := s[registry]; exists {
		return func(_ context.Context, _ string) (orasauth.Credential, error) {
			return orasauth.Credential{Username: creds.Username, Password: creds.Password}, nil
		}
	}

	return nil
}

func (s RegistryCredentialsStore) GetDockerCredsFor(registry string) *dockertypes.DockerAuthConfig {
	if registry == "" {
		return nil
	}

	if creds, exists := s[registry]; exists {
		return &dockertypes.DockerAuthConfig{
			Username: creds.Username,
			Password: creds.Password,
		}
	}

	return nil
}

func (cfg *Config) Login(ctx context.Context, repo, username, password string) error {
	if _, exist := cfg.Credentials[repo]; exist {
		return fmt.Errorf("already logged in, logout first") //nolint:goerr113
	}

	reg, err := remote.NewRegistry(repo)
	if err != nil {
		return fmt.Errorf("parsing registry: %w", err)
	}

	reg.Client = &auth.Client{
		Client: retry.DefaultClient,
		Cache:  nil,
		Credential: func(_ context.Context, _ string) (auth.Credential, error) {
			return auth.Credential{Username: username, Password: password}, nil
		},
	}

	if err := validateCredentials(ctx, reg); err != nil {
		return err
	}

	cfg.Credentials[repo] = RegistryCredentials{
		Username: username,
		Password: password,
	}

	if err := cfg.Credentials.Save(cfg.CredentialsFile); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	slog.Info("Logged in to the registry: " + repo)

	return nil
}

func validateCredentials(ctx context.Context, reg *remote.Registry) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := reg.Ping(ctx); err != nil {
		return fmt.Errorf("validating credentials: %w", err)
	}

	return nil
}

func (cfg *Config) Logout(_ context.Context, repo string) error {
	delete(cfg.Credentials, repo)

	if err := cfg.Credentials.Save(cfg.CredentialsFile); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	slog.Info("Logged out of the registry: " + repo)

	return nil
}
