// Package dockerx wraps the Docker SDK with the small subset of operations the
// runner needs: pull, run, stream logs, and clean up.
package dockerx

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/moby/moby/client"
)

// managedLabel marks containers created by this runner so stale ones can be
// removed at startup.
const managedLabel = "microci.managed"

// Spec describes one isolated build execution.
type Spec struct {
	BuildID      int64
	RepoURL      string
	Branch       string
	Image        string
	SetupCommand string
	TestCommand  string
	CommitSHA    string
}

// Client is a thin wrapper around the official Docker/Moby SDK client.
type Client struct {
	cli    *client.Client
	logger *slog.Logger
	token  string
}

// New connects to the Docker daemon. host may be empty to use the SDK defaults
// (typically the local Unix socket).
func New(host, token string, logger *slog.Logger) (*Client, error) {
	if logger == nil {
		logger = slog.Default()
	}

	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	}

	cli, err := client.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &Client{cli: cli, logger: logger, token: token}, nil
}

// Close releases the Docker client.
func (c *Client) Close() error { return c.cli.Close() }

// Ping checks daemon connectivity.
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.cli.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("ping docker daemon: %w", err)
	}
	return nil
}

// CleanupStale removes containers left behind by a previous process. It is
// best-effort; individual failures are logged and skipped.
func (c *Client) CleanupStale(ctx context.Context) int {
	result, err := c.cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", managedLabel+"=true"),
	})
	if err != nil {
		c.logger.Warn("list stale build containers", "error", err)
		return 0
	}

	removed := 0
	for _, ct := range result.Items {
		_, err := c.cli.ContainerRemove(ctx, ct.ID, client.ContainerRemoveOptions{
			Force:         true,
			RemoveVolumes: true,
		})
		if err != nil {
			c.logger.Warn("remove stale build container", "container", ct.ID, "error", err)
			continue
		}
		removed++
	}
	return removed
}
