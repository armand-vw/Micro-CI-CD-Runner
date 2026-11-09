package sse

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/armand-vw/Micro-CI-CD-Runner/internal/events"
)

type safeRecorder struct {
	mu     sync.Mutex
	header http.Header
	status int
	buf    bytes.Buffer
}

func (r *safeRecorder) Header() http.Header {
	if r.header == nil {
		r.header = make(http.Header)
	}
	return r.header
}

func (r *safeRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(p)
}

func (r *safeRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = status
}

func (r *safeRecorder) Flush() {}

func (r *safeRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func TestStreamsPublishedEvents(t *testing.T) {
	t.Parallel()

	bus := events.NewBus()
	handler := NewHandler(bus, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)

	rec := &safeRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rec, req)
	}()

	deadline := time.Now().Add(time.Second)
	for bus.Subscribers() < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	bus.Publish("build.log", map[string]any{"build_id": 7, "chunk": "hello stream"})

	deadline = time.Now().Add(time.Second)
	for !strings.Contains(rec.body(), "hello stream") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not stop after the request context was cancelled")
	}

	body := rec.body()
	if rec.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.status)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %q", ct)
	}
	for _, want := range []string{": connected", "event: build.log", `"chunk":"hello stream"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}
