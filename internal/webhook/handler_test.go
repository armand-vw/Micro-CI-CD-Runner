package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/armand-vw/Micro-CI-CD-Runner/internal/store"
)

const testSecret = "s3cret-webhook-key"

type fakeStore struct {
	project  store.Project
	findErr  error
	created  bool
	nextID   int64
	finishes []finishCall
}

type finishCall struct {
	id     int64
	status string
	logs   string
}

func (f *fakeStore) FindProjectByRepo(_ context.Context, _ []string) (store.Project, error) {
	if f.findErr != nil {
		return store.Project{}, f.findErr
	}
	return f.project, nil
}

func (f *fakeStore) CreateBuild(_ context.Context, b store.Build) (store.Build, bool, error) {
	b.ID = f.nextID
	return b, f.created, nil
}

func (f *fakeStore) FinishBuild(_ context.Context, id int64, status, _, logs string) error {
	f.finishes = append(f.finishes, finishCall{id: id, status: status, logs: logs})
	return nil
}

type fakeQueue struct {
	ids []int64
	err error
}

func (q *fakeQueue) Enqueue(_ context.Context, id int64) error {
	if q.err != nil {
		return q.err
	}
	q.ids = append(q.ids, id)
	return nil
}

func TestValidSignature(t *testing.T) {
	t.Parallel()

	body := []byte(`{"hello":"world"}`)
	valid := sign(testSecret, body)

	if !ValidSignature([]byte(testSecret), body, valid) {
		t.Fatal("expected valid signature to verify")
	}
	if ValidSignature([]byte(testSecret), []byte(`{}`), valid) {
		t.Fatal("tampered body must not verify")
	}
	if ValidSignature([]byte(testSecret), body, "sha256=deadbeef") {
		t.Fatal("bogus signature must not verify")
	}
	if ValidSignature([]byte(testSecret), body, "") {
		t.Fatal("missing signature must not verify")
	}
	if ValidSignature([]byte(testSecret), body, "sha1="+strings.TrimPrefix(valid, "sha256=")) {
		t.Fatal("wrong algorithm must not verify")
	}
}

func TestHandleQueuesValidPush(t *testing.T) {
	t.Parallel()

	st := &fakeStore{
		project: store.Project{ID: 7, Name: "demo", RepoURL: "github.com/owner/demo", Branch: "main"},
		created: true,
		nextID:  42,
	}
	q := &fakeQueue{}
	h := NewHandler(testSecret, st, q, slog.Default())

	body := pushBody("refs/heads/main", strings.Repeat("a", 40), "https://github.com/owner/demo.git")
	rec := serve(t, h, body, sign(testSecret, body), "push", "delivery-1")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if len(q.ids) != 1 || q.ids[0] != 42 {
		t.Fatalf("queued ids = %v, want [42]", q.ids)
	}
}

func TestHandleRejectsBadSignature(t *testing.T) {
	t.Parallel()

	st := &fakeStore{created: true, nextID: 1}
	q := &fakeQueue{}
	h := NewHandler(testSecret, st, q, slog.Default())

	body := pushBody("refs/heads/main", strings.Repeat("a", 40), "https://github.com/owner/demo.git")
	rec := serve(t, h, body, "sha256=00", "push", "delivery-1")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if len(q.ids) != 0 {
		t.Fatal("invalid signature must not enqueue a build")
	}
}

func TestHandleIgnoresOtherBranch(t *testing.T) {
	t.Parallel()

	st := &fakeStore{
		project: store.Project{ID: 7, Name: "demo", Branch: "main"},
		created: true,
		nextID:  1,
	}
	q := &fakeQueue{}
	h := NewHandler(testSecret, st, q, slog.Default())

	body := pushBody("refs/heads/feature", strings.Repeat("b", 40), "https://github.com/owner/demo.git")
	rec := serve(t, h, body, sign(testSecret, body), "push", "delivery-2")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if len(q.ids) != 0 {
		t.Fatal("branch mismatch must not enqueue a build")
	}
}

func TestHandlePing(t *testing.T) {
	t.Parallel()

	h := NewHandler(testSecret, &fakeStore{}, &fakeQueue{}, slog.Default())
	body := []byte(`{"zen":"Keep it logically awesome."}`)
	rec := serve(t, h, body, sign(testSecret, body), "ping", "delivery-3")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestHandleIgnoresDeletedBranch(t *testing.T) {
	t.Parallel()

	st := &fakeStore{project: store.Project{ID: 7, Branch: "main"}, created: true, nextID: 1}
	q := &fakeQueue{}
	h := NewHandler(testSecret, st, q, slog.Default())

	body := pushBody("refs/heads/main", strings.Repeat("0", 40), "https://github.com/owner/demo.git")
	rec := serve(t, h, body, sign(testSecret, body), "push", "delivery-4")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(q.ids) != 0 {
		t.Fatal("deleted branch must not enqueue a build")
	}
}

func TestHandleDuplicateDelivery(t *testing.T) {
	t.Parallel()

	st := &fakeStore{
		project: store.Project{ID: 7, Branch: "main"},
		created: false,
		nextID:  99,
	}
	q := &fakeQueue{}
	h := NewHandler(testSecret, st, q, slog.Default())

	body := pushBody("refs/heads/main", strings.Repeat("c", 40), "https://github.com/owner/demo.git")
	rec := serve(t, h, body, sign(testSecret, body), "push", "delivery-5")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if len(q.ids) != 0 {
		t.Fatal("duplicate delivery must not enqueue a second build")
	}
}

func TestHandleQueueFullFailsBuild(t *testing.T) {
	t.Parallel()

	st := &fakeStore{
		project: store.Project{ID: 7, Branch: "main"},
		created: true,
		nextID:  55,
	}
	q := &fakeQueue{err: errors.New("queue full")}
	h := NewHandler(testSecret, st, q, slog.Default())

	body := pushBody("refs/heads/main", strings.Repeat("d", 40), "https://github.com/owner/demo.git")
	rec := serve(t, h, body, sign(testSecret, body), "push", "delivery-6")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if len(st.finishes) != 1 || st.finishes[0].id != 55 || st.finishes[0].status != store.StatusFailed {
		t.Fatalf("expected rejected build to be marked failed, got %+v", st.finishes)
	}
}

func TestHandleMissingEventHeader(t *testing.T) {
	t.Parallel()

	h := NewHandler(testSecret, &fakeStore{}, &fakeQueue{}, slog.Default())
	body := pushBody("refs/heads/main", strings.Repeat("e", 40), "https://github.com/owner/demo.git")
	rec := serve(t, h, body, sign(testSecret, body), "", "delivery-7")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func serve(t *testing.T, h http.Handler, body []byte, signature, event, delivery string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	req.Header.Set(signatureHeader, signature)
	if event != "" {
		req.Header.Set(eventHeader, event)
	}
	if delivery != "" {
		req.Header.Set(deliveryHeader, delivery)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func pushBody(ref, after, cloneURL string) []byte {
	payload := map[string]any{
		"ref":    ref,
		"after":  after,
		"before": strings.Repeat("f", 40),
		"repository": map[string]any{
			"full_name": "owner/demo",
			"clone_url": cloneURL,
			"ssh_url":   "git@github.com:owner/demo.git",
			"html_url":  "https://github.com/owner/demo",
		},
		"head_commit": map[string]any{"id": after},
	}
	body, _ := json.Marshal(payload)
	return body
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
