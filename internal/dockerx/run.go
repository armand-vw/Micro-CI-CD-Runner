package dockerx

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	workspaceDir  = "/workspace"
	workspaceSize = "512m"
)

// Run pulls the image, starts an isolated container, clones the repository,
// runs the configured test command, and streams combined stdout/stderr to
// write. It returns the container exit code. Infrastructure failures are
// returned as errors; a non-zero exit code is not an error.
func (c *Client) Run(ctx context.Context, spec Spec, write func(string)) (int, error) {
	if spec.Image == "" {
		return 0, fmt.Errorf("build image is empty")
	}
	if spec.RepoURL == "" {
		return 0, fmt.Errorf("repository URL is empty")
	}
	if write == nil {
		write = func(string) {}
	}

	if err := c.ensureImage(ctx, spec.Image); err != nil {
		return 0, err
	}

	name := fmt.Sprintf("microci-%d-%d", spec.BuildID, time.Now().UnixNano())
	cfg := &container.Config{
		Image:        spec.Image,
		Cmd:          []string{"/bin/sh", "-c", buildScript(spec, c.token)},
		WorkingDir:   workspaceDir,
		Env:          buildEnv(spec, c.token),
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Labels: map[string]string{
			managedLabel:         "true",
			"microci.build_id":   strconv.FormatInt(spec.BuildID, 10),
			"microci.commit_sha": spec.CommitSHA,
			"microci.repo_url":   spec.RepoURL,
		},
	}
	hostCfg := &container.HostConfig{
		AutoRemove: false,
		Tmpfs: map[string]string{
			workspaceDir: "rw,size=" + workspaceSize + ",mode=1777",
		},
		Resources: container.Resources{
			Memory:    512 * 1024 * 1024,
			NanoCPUs:  1_000_000_000,
			PidsLimit: int64Ptr(256),
		},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"},
		NetworkMode: container.NetworkMode("bridge"),
	}

	created, err := c.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     cfg,
		HostConfig: hostCfg,
		Name:       name,
	})
	if err != nil {
		return 0, fmt.Errorf("create build container: %w", err)
	}
	defer c.removeContainer(created.ID)

	if _, err := c.cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return 0, fmt.Errorf("start build container: %w", err)
	}

	logs, err := c.cli.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return 0, fmt.Errorf("stream build logs: %w", err)
	}
	defer logs.Close()

	sink := writerFunc(write)
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		// StdCopy de-multiplexes the Docker stream. We deliberately merge
		// stdout and stderr into one ordered dashboard log.
		_, _ = stdcopy.StdCopy(sink, sink, logs)
	}()

	wait := c.cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{
		Condition: container.WaitConditionNotRunning,
	})

	var (
		exitCode  int
		waitErr   error
		gotStatus bool
	)
	resultCh, errorCh := wait.Result, wait.Error
	for resultCh != nil || errorCh != nil {
		select {
		case <-ctx.Done():
			killCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_, _ = c.cli.ContainerKill(killCtx, created.ID, client.ContainerKillOptions{Signal: "SIGKILL"})
			cancel()
			waitForLogs(copyDone)
			return 0, ctx.Err()
		case res, ok := <-resultCh:
			if !ok {
				resultCh = nil
				continue
			}
			exitCode = int(res.StatusCode)
			gotStatus = true
			resultCh, errorCh = nil, nil
		case err, ok := <-errorCh:
			if !ok {
				errorCh = nil
				continue
			}
			waitErr = err
			resultCh, errorCh = nil, nil
		}
	}

	if waitErr != nil {
		return 0, fmt.Errorf("wait for build container: %w", waitErr)
	}
	if !gotStatus {
		return 0, fmt.Errorf("container wait ended without a status")
	}

	waitForLogs(copyDone)
	return exitCode, nil
}

func (c *Client) ensureImage(ctx context.Context, ref string) error {
	if _, err := c.cli.ImageInspect(ctx, ref); err == nil {
		return nil
	}

	reader, err := c.cli.ImagePull(ctx, ref, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull image %q: %w", ref, err)
	}
	defer reader.Close()

	if _, err := io.Copy(io.Discard, reader); err != nil {
		return fmt.Errorf("pull image %q: %w", ref, err)
	}
	return nil
}

func (c *Client) removeContainer(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{
		Force:         true,
		RemoveVolumes: true,
	}); err != nil {
		c.logger.Warn("remove build container", "container", id, "error", err)
	}
}

func buildScript(spec Spec, token string) string {
	cloneURL := spec.RepoURL
	if !strings.Contains(cloneURL, "://") {
		cloneURL = "https://" + cloneURL
	}
	if !strings.HasSuffix(cloneURL, ".git") {
		cloneURL += ".git"
	}

	branch := spec.Branch
	if branch == "" {
		branch = "main"
	}

	var b strings.Builder
	b.WriteString("set -eu\n")
	if token != "" {
		// Keep the token out of the clone URL and out of the container's git
		// config by supplying it through a credential helper.
		b.WriteString("git config --global credential.helper '!f() { echo \"username=x-access-token\"; echo \"password=$GITHUB_TOKEN\"; }; f'\n")
	}
	if spec.SetupCommand != "" {
		b.WriteString(spec.SetupCommand)
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "git clone --depth 1 --branch %s %s %s/repo\n", shellQuote(branch), shellQuote(cloneURL), workspaceDir)
	fmt.Fprintf(&b, "cd %s/repo\n", workspaceDir)
	if spec.TestCommand != "" {
		b.WriteString(spec.TestCommand)
		b.WriteByte('\n')
	}
	return b.String()
}

func buildEnv(spec Spec, token string) []string {
	env := []string{
		"CI=true",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=/tmp",
		"GITHUB_SHA=" + spec.CommitSHA,
		"GITHUB_REF=refs/heads/" + spec.Branch,
		"MICROCI_BUILD_ID=" + strconv.FormatInt(spec.BuildID, 10),
	}
	if token != "" {
		env = append(env, "GITHUB_TOKEN="+token)
	}
	return env
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func int64Ptr(v int64) *int64 { return &v }

type writerFunc func(string)

func (f writerFunc) Write(p []byte) (int, error) {
	if len(p) > 0 {
		f(string(p))
	}
	return len(p), nil
}

func waitForLogs(done <-chan struct{}) {
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}
