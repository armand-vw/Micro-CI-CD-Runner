// Package webhook validates and ingests GitHub push events.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/armand-vw/Micro-CI-CD-Runner/internal/giturl"
	"github.com/armand-vw/Micro-CI-CD-Runner/internal/store"
)

const (
	signatureHeader = "X-Hub-Signature-256"
	eventHeader     = "X-GitHub-Event"
	deliveryHeader  = "X-GitHub-Delivery"
	maxBodyBytes    = 1 << 20 // 1 MiB is plenty for a push payload.
)

// Store is the subset of persistence the webhook needs.
type Store interface {
	FindProjectByRepo(ctx context.Context, repos []string) (store.Project, error)
	CreateBuild(ctx context.Context, b store.Build) (store.Build, bool, error)
	FinishBuild(ctx context.Context, id int64, status, finishedAt, logs string) error
}

// Enqueuer accepts build IDs for asynchronous execution.
type Enqueuer interface {
	Enqueue(ctx context.Context, buildID int64) error
}

// Publisher broadcasts dashboard events. It is optional.
type Publisher interface {
	Publish(eventType string, data any)
}

// Handler validates GitHub webhooks and turns push events into queued builds.
type Handler struct {
	secret    []byte
	store     Store
	queue     Enqueuer
	publisher Publisher
	logger    *slog.Logger
	maxBody   int64
}

// NewHandler builds a webhook handler. The secret must be non-empty; the
// process refuses to start without one (see internal/config).
func NewHandler(secret string, st Store, queue Enqueuer, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		secret:  []byte(secret),
		store:   st,
		queue:   queue,
		logger:  logger,
		maxBody: maxBodyBytes,
	}
}

// WithPublisher attaches an event publisher so newly queued builds appear on
// the dashboard immediately.
func (h *Handler) WithPublisher(p Publisher) *Handler {
	h.publisher = p
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, response{Status: "error", Reason: "method not allowed"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSON(w, http.StatusRequestEntityTooLarge, response{Status: "error", Reason: "payload too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, response{Status: "error", Reason: "cannot read payload"})
		return
	}

	if !ValidSignature(h.secret, body, r.Header.Get(signatureHeader)) {
		h.logger.Warn("rejected webhook with invalid signature",
			"remote", r.RemoteAddr,
			"delivery", r.Header.Get(deliveryHeader),
		)
		writeJSON(w, http.StatusUnauthorized, response{Status: "error", Reason: "invalid signature"})
		return
	}

	event := strings.TrimSpace(r.Header.Get(eventHeader))
	if event == "" {
		writeJSON(w, http.StatusBadRequest, response{Status: "error", Reason: "missing event header"})
		return
	}

	// GitHub sends a ping event when the webhook is first configured.
	if event == "ping" {
		writeJSON(w, http.StatusOK, response{Status: "pong"})
		return
	}
	if event != "push" {
		writeJSON(w, http.StatusOK, response{Status: "ignored", Reason: "unsupported event: " + event})
		return
	}

	var payload pushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, response{Status: "error", Reason: "invalid JSON payload"})
		return
	}

	h.handlePush(w, r, payload)
}

func (h *Handler) handlePush(w http.ResponseWriter, r *http.Request, p pushPayload) {
	if p.Deleted || isZeroSHA(p.After) {
		writeJSON(w, http.StatusOK, response{Status: "ignored", Reason: "branch deleted or empty push"})
		return
	}

	branch := strings.TrimPrefix(p.Ref, "refs/heads/")
	if branch == p.Ref {
		writeJSON(w, http.StatusOK, response{Status: "ignored", Reason: "not a branch push"})
		return
	}

	commit := p.After
	if commit == "" {
		commit = p.HeadCommit.ID
	}
	if commit == "" {
		writeJSON(w, http.StatusBadRequest, response{Status: "error", Reason: "missing commit SHA"})
		return
	}

	candidates := make([]string, 0, 3)
	for _, raw := range []string{p.Repository.CloneURL, p.Repository.SSHURL, p.Repository.HTMLURL} {
		if normalized := giturl.Normalize(raw); normalized != "" {
			candidates = append(candidates, normalized)
		}
	}

	project, err := h.store.FindProjectByRepo(r.Context(), candidates)
	if errors.Is(err, store.ErrNotFound) {
		h.logger.Info("ignoring push for unregistered repository",
			"repository", p.Repository.FullName,
			"branch", branch,
		)
		writeJSON(w, http.StatusAccepted, response{Status: "ignored", Reason: "repository is not registered"})
		return
	}
	if err != nil {
		h.logger.Error("look up project", "error", err, "repository", p.Repository.FullName)
		writeJSON(w, http.StatusInternalServerError, response{Status: "error", Reason: "database error"})
		return
	}

	if project.Branch != "" && project.Branch != "*" && project.Branch != branch {
		writeJSON(w, http.StatusAccepted, response{Status: "ignored", Reason: "branch does not match project configuration"})
		return
	}

	build, created, err := h.store.CreateBuild(r.Context(), store.Build{
		ProjectID:  project.ID,
		CommitSHA:  commit,
		Ref:        p.Ref,
		Status:     store.StatusPending,
		DeliveryID: r.Header.Get(deliveryHeader),
	})
	if err != nil {
		h.logger.Error("create build", "error", err, "project", project.Name)
		writeJSON(w, http.StatusInternalServerError, response{Status: "error", Reason: "database error"})
		return
	}
	if !created {
		writeJSON(w, http.StatusAccepted, response{Status: "duplicate", BuildID: build.ID})
		return
	}

	if err := h.queue.Enqueue(r.Context(), build.ID); err != nil {
		note := "\nrunner queue is full; build was not started\n"
		if finishErr := h.store.FinishBuild(r.Context(), build.ID, store.StatusFailed, "", note); finishErr != nil {
			h.logger.Error("mark rejected build failed", "error", finishErr, "build_id", build.ID)
		}
		h.logger.Warn("build queue full", "build_id", build.ID, "project", project.Name)
		writeJSON(w, http.StatusServiceUnavailable, response{Status: "error", Reason: "build queue is full"})
		return
	}

	build.ProjectName = project.Name
	build.Logs = ""
	if h.publisher != nil {
		h.publisher.Publish("build.created", build)
	}

	h.logger.Info("queued build",
		"build_id", build.ID,
		"project", project.Name,
		"branch", branch,
		"commit", commit,
	)
	writeJSON(w, http.StatusAccepted, response{Status: "queued", BuildID: build.ID})
}

// ValidSignature checks a GitHub "sha256=<hex>" signature in constant time.
func ValidSignature(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}

	got, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

type pushPayload struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	HeadCommit struct {
		ID string `json:"id"`
	} `json:"head_commit"`
	Repository struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
		SSHURL   string `json:"ssh_url"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

type response struct {
	Status  string `json:"status"`
	BuildID int64  `json:"build_id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func isZeroSHA(sha string) bool {
	return sha == "" || strings.Trim(sha, "0") == ""
}
