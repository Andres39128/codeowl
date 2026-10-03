package github

// Tests de FetchPRTimeline (§6 F5): commits con fecha de autoría + patch,
// threads con resolución vía GraphQL, topes y semántica best-effort. Stub
// httptest de toda la superficie (instalación, token de instalación,
// commits, patch, graphql) — sin salida a internet ni BD: el token manager
// se arma con una private key RSA efímera generada en el test.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// timelineStub es el stub de la API de GitHub para la timeline: rutea los
// cinco endpoints que FetchPRTimeline consume y graba los encabezados que
// los tests asertan (Accept del diff, Bearer del graphql).
type timelineStub struct {
	mu sync.Mutex

	commits []ghAPICommit      // fixture del listado de commits
	patches map[string]string  // sha → diff crudo; sha ausente ⇒ 500
	diffAccept map[string]string // sha → Accept recibido en /commits/{sha}

	graphqlStatus int
	graphqlBody   string
	graphqlAuth   string
}

func (s *timelineStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.URL.Path == "/repos/acme/api/installation":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 42})
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/app/installations/"):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated) // el intercambio de token exige 201
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "inst-token",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	case r.URL.Path == "/repos/acme/api/pulls/9/commits":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.commits)
	case strings.HasPrefix(r.URL.Path, "/repos/acme/api/commits/"):
		sha := strings.TrimPrefix(r.URL.Path, "/repos/acme/api/commits/")
		s.diffAccept[sha] = r.Header.Get("Accept")
		patch, ok := s.patches[sha]
		if !ok {
			http.Error(w, "sin patch para el sha", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(patch))
	case r.URL.Path == "/graphql":
		s.graphqlAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.graphqlStatus)
		_, _ = w.Write([]byte(s.graphqlBody))
	default:
		http.NotFound(w, r)
	}
}

// newTimelineEnv arma adapter + stub: baseURL apuntando al stub y token
// manager con private key efímera (la firma del JWT valida contra el stub).
func newTimelineEnv(t *testing.T, st *timelineStub) *Adapter {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generando RSA: %v", err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
	cfg := &config.Config{Stage2: config.Stage2Config{
		GitHubAppID:         "1",
		GitHubAppPrivateKey: pemKey,
	}}
	srv := httptest.NewServer(st)
	t.Cleanup(srv.Close)
	ad := New(nil, cfg, nil)
	ad.baseURL = srv.URL // sobrescribible en tests (stub server), adapter.go
	return ad
}

func timelineRepoAndPR() (*store.Repository, *store.PullRequest) {
	return &store.Repository{Vcs: "github", Owner: "acme", Name: "api"},
		&store.PullRequest{Number: 9, BaseRef: "main", BaseSha: "b", HeadSha: "h"}
}

func ghCommit(sha, msg, author, date string) ghAPICommit {
	c := ghAPICommit{SHA: sha}
	c.Commit.Message = msg
	c.Commit.Author.Name = author
	c.Commit.Author.Date = date
	return c
}

const happyGraphQLBody = `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[` +
	`{"isResolved":true,"comments":{"nodes":[{"databaseId":101},{"databaseId":102}]}},` +
	`{"isResolved":false,"comments":{"nodes":[{"databaseId":103}]}}` +
	`]}}}}}`

func TestFetchPRTimelinePatchesDatesThreads(t *testing.T) {
	st := &timelineStub{
		commits: []ghAPICommit{
			ghCommit("sha-1", "primer commit", "Ana", "2026-10-01T10:00:00Z"),
			ghCommit("sha-2", "segundo commit", "Beto", "2026-10-02T11:30:00Z"),
		},
		patches: map[string]string{
			"sha-1": "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-old\n+new\n",
			"sha-2": "diff --git a/y.go b/y.go\n--- a/y.go\n+++ b/y.go\n@@ -1 +1 @@\n-old\n+newer\n",
		},
		diffAccept:    map[string]string{},
		graphqlStatus: http.StatusOK,
		graphqlBody:   happyGraphQLBody,
	}
	ad := newTimelineEnv(t, st)
	repo, pr := timelineRepoAndPR()

	tl, err := ad.FetchPRTimeline(context.Background(), repo, pr)
	if err != nil {
		t.Fatalf("FetchPRTimeline: %v", err)
	}
	if len(tl.Commits) != 2 {
		t.Fatalf("commits: got %d want 2", len(tl.Commits))
	}
	wantDate, _ := time.Parse(time.RFC3339, "2026-10-01T10:00:00Z")
	if !tl.Commits[0].AuthoredAt.Equal(wantDate) {
		t.Errorf("AuthoredAt: got %v want %v", tl.Commits[0].AuthoredAt, wantDate)
	}
	if tl.Commits[0].Author != "Ana" || tl.Commits[0].Message != "primer commit" {
		t.Errorf("commit 0: %+v", tl.Commits[0])
	}
	if got := tl.Commits[0].Patch; !strings.HasPrefix(got, "diff --git a/x.go b/x.go") {
		t.Errorf("patch del commit 0 incompleto: %q", got)
	}
	if tl.Commits[1].Patch == "" {
		t.Error("patch del commit 2 vacío")
	}
	if accept := st.diffAccept["sha-1"]; accept != mediaTypeDiff {
		t.Errorf("Accept del fetch de patch: got %q want %q", accept, mediaTypeDiff)
	}

	// databaseId del comentario de review = CommentID persistido por
	// PostInlineComment; la resolución del hilo cubre a todos sus comentarios.
	wantThreads := []vcs.PRThread{
		{CommentID: "101", Resolved: true},
		{CommentID: "102", Resolved: true},
		{CommentID: "103", Resolved: false},
	}
	if len(tl.Threads) != len(wantThreads) {
		t.Fatalf("threads: got %d want %d (%+v)", len(tl.Threads), len(wantThreads), tl.Threads)
	}
	for i, w := range wantThreads {
		if tl.Threads[i] != w {
			t.Errorf("thread %d: got %+v want %+v", i, tl.Threads[i], w)
		}
	}
	if auth := st.graphqlAuth; auth != "Bearer inst-token" {
		t.Errorf("Authorization del graphql: got %q want el token de instalación", auth)
	}
}

// TestFetchPRTimelineCaps verifica los topes del contrato (§6 F5): 50
// commits aunque la API devuelva más, y patch truncado a 64KB.
func TestFetchPRTimelineCaps(t *testing.T) {
	st := &timelineStub{patches: map[string]string{}, diffAccept: map[string]string{}}
	for i := 0; i < 60; i++ { // más que vcs.MaxTimelineCommits
		st.commits = append(st.commits, ghCommit(fmt.Sprintf("sha-%02d", i), "m", "a", "2026-10-01T10:00:00Z"))
	}
	st.patches["sha-00"] = strings.Repeat("x", 80*1024) // más que MaxPatchBytes
	ad := newTimelineEnv(t, st)
	repo, pr := timelineRepoAndPR()

	tl, err := ad.FetchPRTimeline(context.Background(), repo, pr)
	if err != nil {
		t.Fatalf("FetchPRTimeline: %v", err)
	}
	if len(tl.Commits) != vcs.MaxTimelineCommits {
		t.Fatalf("tope de commits: got %d want %d", len(tl.Commits), vcs.MaxTimelineCommits)
	}
	if len(tl.Commits[0].Patch) != vcs.MaxPatchBytes {
		t.Fatalf("tope de patch: got %d want %d", len(tl.Commits[0].Patch), vcs.MaxPatchBytes)
	}
}

// TestFetchPRTimelineBestEffort verifica la degradación (§6 F5): patch de un
// commit fallido ⇒ patch vacío sin error; threads fallidos (HTTP 500 o
// errores GraphQL) ⇒ timeline completa con Threads vacío.
func TestFetchPRTimelineBestEffort(t *testing.T) {
	st := &timelineStub{
		commits: []ghAPICommit{ghCommit("sha-1", "m", "a", "2026-10-01T10:00:00Z")},
		patches: map[string]string{}, // todo /commits/{sha} responde 500
		diffAccept: map[string]string{},
	}
	ad := newTimelineEnv(t, st)
	repo, pr := timelineRepoAndPR()

	// Silenciar el log de best-effort esperado en la salida del test.
	slog.SetDefault(slog.New(slog.DiscardHandler))

	tl, err := ad.FetchPRTimeline(context.Background(), repo, pr)
	if err != nil {
		t.Fatalf("patch fallido no debe fallar la timeline: %v", err)
	}
	if tl.Commits[0].Patch != "" {
		t.Errorf("patch debe quedar vacío, got %q", tl.Commits[0].Patch)
	}
	if len(tl.Threads) != 0 {
		t.Errorf("threads vacíos ante graphql 500, got %+v", tl.Threads)
	}

	// Fallo semántico de GraphQL (200 con errors): mismo tratamiento.
	st.mu.Lock()
	st.graphqlStatus = http.StatusOK
	st.graphqlBody = `{"errors":[{"message":"forbidden"}]}`
	st.mu.Unlock()

	tl, err = ad.FetchPRTimeline(context.Background(), repo, pr)
	if err != nil {
		t.Fatalf("errores GraphQL no deben fallar la timeline: %v", err)
	}
	if len(tl.Threads) != 0 {
		t.Errorf("threads vacíos ante errores GraphQL, got %+v", tl.Threads)
	}
	if len(tl.Commits) != 1 || tl.Commits[0].SHA != "sha-1" {
		t.Errorf("los commits sobreviven al fallo de threads: %+v", tl.Commits)
	}
}
