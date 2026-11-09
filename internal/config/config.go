// Package config loads, validates, and applies defaults to runtime settings.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/armand-vw/Micro-CI-CD-Runner/internal/giturl"
)

// Config holds every runtime setting for the runner.
type Config struct {
	Port                string
	DBPath              string
	ProjectsFile        string
	WebhookSecret       string
	GitHubToken         string
	DockerHost          string
	MaxConcurrentBuilds int
	QueueSize           int
	BuildTimeout        time.Duration
	LogMaxBytes         int
	AdminToken          string
	PublicBaseURL       string
	AllowedOrigins      []string
	LogLevel            slog.Level
}

// Load reads configuration from the environment and fails fast on invalid or
// missing required values.
func Load() (*Config, error) {
	c := &Config{
		Port:           env("PORT", "8080"),
		DBPath:         env("DB_PATH", "./data/runner.db"),
		ProjectsFile:   env("PROJECTS_FILE", "./config/projects.yaml"),
		WebhookSecret:  os.Getenv("GITHUB_WEBHOOK_SECRET"),
		GitHubToken:    os.Getenv("GITHUB_TOKEN"),
		DockerHost:     env("DOCKER_HOST", ""),
		AdminToken:     os.Getenv("ADMIN_TOKEN"),
		PublicBaseURL:  strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		AllowedOrigins: splitCSV(os.Getenv("ALLOWED_ORIGINS")),
	}

	var err error
	if c.MaxConcurrentBuilds, err = envInt("MAX_CONCURRENT_BUILDS", 1); err != nil {
		return nil, err
	}
	if c.QueueSize, err = envInt("QUEUE_SIZE", 100); err != nil {
		return nil, err
	}
	if c.LogMaxBytes, err = envInt("LOG_MAX_BYTES", 2*1024*1024); err != nil {
		return nil, err
	}
	if c.BuildTimeout, err = envDuration("BUILD_TIMEOUT", 15*time.Minute); err != nil {
		return nil, err
	}
	if c.LogLevel, err = parseLevel(env("LOG_LEVEL", "info")); err != nil {
		return nil, err
	}

	if c.WebhookSecret == "" {
		return nil, fmt.Errorf("GITHUB_WEBHOOK_SECRET is required")
	}
	if c.MaxConcurrentBuilds < 1 {
		return nil, fmt.Errorf("MAX_CONCURRENT_BUILDS must be at least 1")
	}
	if c.QueueSize < 1 {
		return nil, fmt.Errorf("QUEUE_SIZE must be at least 1")
	}
	if c.LogMaxBytes < 1024 {
		return nil, fmt.Errorf("LOG_MAX_BYTES must be at least 1024")
	}
	if c.BuildTimeout <= 0 {
		return nil, fmt.Errorf("BUILD_TIMEOUT must be positive")
	}

	return c, nil
}

// ProjectSeed is one entry in the projects YAML file.
type ProjectSeed struct {
	Name         string `yaml:"name"`
	RepoURL      string `yaml:"repo_url"`
	Branch       string `yaml:"branch"`
	Image        string `yaml:"image"`
	SetupCommand string `yaml:"setup_command"`
	TestCommand  string `yaml:"test_command"`
}

// LoadProjects parses and validates the projects YAML file. Repository URLs are
// normalised so webhook lookups are stable across clone/ssh/https forms.
func LoadProjects(path string) ([]ProjectSeed, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read projects file: %w", err)
	}

	var file struct {
		Projects []ProjectSeed `yaml:"projects"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse projects file: %w", err)
	}

	for i := range file.Projects {
		p := &file.Projects[i]
		p.Name = strings.TrimSpace(p.Name)
		p.RepoURL = giturl.Normalize(p.RepoURL)
		p.Branch = strings.TrimSpace(p.Branch)
		p.Image = strings.TrimSpace(p.Image)
		p.SetupCommand = strings.TrimSpace(p.SetupCommand)
		p.TestCommand = strings.TrimSpace(p.TestCommand)

		if p.Name == "" {
			return nil, fmt.Errorf("projects[%d]: name is required", i)
		}
		if p.RepoURL == "" {
			return nil, fmt.Errorf("projects[%d]: repo_url is required", i)
		}
		if p.Branch == "" {
			p.Branch = "main"
		}
		if p.Image == "" {
			p.Image = "node:20-alpine"
		}
		if p.SetupCommand == "" {
			p.SetupCommand = "apk add --no-cache git"
		}
		if p.TestCommand == "" {
			p.TestCommand = "npm ci && npm test"
		}
	}

	return file.Projects, nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	raw := env(key, "")
	if raw == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return v, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := env(key, "")
	if raw == "" {
		return fallback, nil
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration (e.g. 15m): %w", key, err)
	}
	return v, nil
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error")
	}
}
