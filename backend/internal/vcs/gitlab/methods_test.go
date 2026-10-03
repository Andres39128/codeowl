package gitlab

// Tests de FetchPRTimeline (§6 F5): commits con fecha de autoría + patch
// reensamblado, discusiones inline con resolución, topes y semántica
// best-effort. Stub httptest de la API v4 (commits, diff por commit,
// discussions) — sin salida a internet ni BD: el api token se cifra con la
// master key de test y la instancia se apunta a base_url.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// timelineStub es el stub de la API v4 de GitLab para la timeline: rutea los
// tres endpoints que FetchPRTimeline consume. Los fixtures viajan como JSON
// crudo (el shape anidado de discussions no se modela dos veces).
type timelineStub struct {
	mu sync.Mutex

	commitsJSON    string // GET .../commits
	commitDiff     map[string]string
	commitDiffCode map[string]int // sha → status; default 200 si hay fixture
	discussions    struct {
		code int
		body string
	}
}

func (s *timelineStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	const commitsPath = "/api/v4/projects/7/merge_requests/5/commits"
	const discussionsPath = "/api/v4/projects/7/merge_requests/5/discussions"
	switch {
	case r.URL.Path == commitsPath:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(s.commitsJSON))
	case strings.HasPrefix(r.URL.Path, "/api/v4/projects/7/repository/commits/"):
		sha := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v4/projects/7/repository/commits/"), "/diff")
		if code, ok := s.commitDiffCode[sha]; ok && code != http.StatusOK {
			w.WriteHeader(code)
			return
		}
		body, ok := s.commitDiff[sha]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	case r.URL.Path == discussionsPath:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.discussions.code)
		_, _ = w.Write([]byte(s.discussions.body))
	default:
		http.NotFound(w, r)
	}
}

// newTimelineEnv arma adapter + repo apuntando al stub: base_url del repo es
// la URL del httptest y el api token va cifrada con la master key de test.
func newTimelineEnv(t *testing.T, st *timelineStub) (*Adapter, *store.Repository) {
	t.Helper()
	srv := httptest.NewServer(st)
	t.Cleanup(srv.Close)
	cfg := &config.Config{MasterKey: testMasterKey}
	enc, err := store.Encrypt(testMasterKey, []byte("glpat-timeline-test"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	repo := &store.Repository{
		Vcs:        "gitlab",
		ExternalID: 7,
		Owner:      "acme",
		Name:       "api",
		BaseUrl:    pgtype.Text{String: srv.URL, Valid: true},
		ApiToken:   pgtype.Text{String: enc, Valid: true},
	}
	return New(nil, cfg, nil), repo
}

func timelinePR() *store.PullRequest {
	return &store.PullRequest{Number: 5, BaseRef: "main", BaseSha: "b", HeadSha: "h"}
}

func glCommitJSON(id, date string) string {
	return fmt.Sprintf(`{"id":%q,"message":"m","author_name":"Ana","authored_date":%q}`, id, date)
}

const aGoDiff = `[{"old_path":"a.go","new_path":"a.go","diff":"@@ -1 +1 @@\n-old\n+new\n"}]`

// TestFetchPRTimelinePatchesThreads verifica el mapeo feliz (§6 F5): fecha
// parseada, patch reensamblado con encabezados git, threads solo de notas
// inline (position presente) no-system, CommentID = id de la DISCUSIÓN (lo
// que postDiscussion persiste en comments_sent), resolved ausente ⇒ false.
func TestFetchPRTimelinePatchesThreads(t *testing.T) {
	st := &timelineStub{
		commitsJSON: "[" +
			glCommitJSON("c1", "2026-10-01T10:00:00Z") + "," +
			glCommitJSON("c2", "no-es-una-fecha") + "]",
		commitDiff:     map[string]string{"c1": aGoDiff},
		commitDiffCode: map[string]int{"c2": http.StatusInternalServerError},
	}
	st.discussions.code = http.StatusOK
	st.discussions.body = `[
		{"id":"disc-inline-1","notes":[
			{"id":1,"system":false,"position":{"position_type":"text"},"resolved":true},
			{"id":2,"system":true}
		]},
		{"id":"disc-general","notes":[{"id":3}]},
		{"id":"disc-inline-2","notes":[{"id":4,"position":{"position_type":"text"}}]},
		{"id":"disc-reply","notes":[
			{"id":5,"position":{"position_type":"text"},"resolved":false},
			{"id":6,"position":{"position_type":"text"},"resolved":true}
		]}
	]`
	ad, repo := newTimelineEnv(t, st)

	// Silenciar los logs de best-effort esperados en la salida del test.
	slog.SetDefault(slog.New(slog.DiscardHandler))

	tl, err := ad.FetchPRTimeline(context.Background(), repo, timelinePR())
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
	if !tl.Commits[1].AuthoredAt.IsZero() {
		t.Errorf("fecha no-ISO debe quedar en cero, got %v", tl.Commits[1].AuthoredAt)
	}
	wantPatch := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"
	if tl.Commits[0].Patch != wantPatch {
		t.Errorf("patch reensamblado:\n got %q\nwant %q", tl.Commits[0].Patch, wantPatch)
	}
	if tl.Commits[1].Patch != "" {
		t.Errorf("patch del commit con diff 500 debe ser vacío, got %q", tl.Commits[1].Patch)
	}

	wantThreads := []vcs.PRThread{
		{CommentID: "disc-inline-1", Resolved: true},
		{CommentID: "disc-inline-2", Resolved: false}, // resolved ausente ⇒ false honesto
		{CommentID: "disc-reply", Resolved: false},    // la primera nota inline representa
	}
	if len(tl.Threads) != len(wantThreads) {
		t.Fatalf("threads: got %d want %d (%+v)", len(tl.Threads), len(wantThreads), tl.Threads)
	}
	for i, w := range wantThreads {
		if tl.Threads[i] != w {
			t.Errorf("thread %d: got %+v want %+v", i, tl.Threads[i], w)
		}
	}
}

// TestFetchPRTimelineCaps verifica el tope del contrato (§6 F5): 50 commits
// aunque la API devuelva más.
func TestFetchPRTimelineCaps(t *testing.T) {
	var commits []string
	for i := 0; i < 60; i++ { // más que vcs.MaxTimelineCommits
		commits = append(commits, glCommitJSON(fmt.Sprintf("c%02d", i), "2026-10-01T10:00:00Z"))
	}
	st := &timelineStub{commitsJSON: "[" + strings.Join(commits, ",") + "]"}
	ad, repo := newTimelineEnv(t, st)
	slog.SetDefault(slog.New(slog.DiscardHandler))

	tl, err := ad.FetchPRTimeline(context.Background(), repo, timelinePR())
	if err != nil {
		t.Fatalf("FetchPRTimeline: %v", err)
	}
	if len(tl.Commits) != vcs.MaxTimelineCommits {
		t.Fatalf("tope de commits: got %d want %d", len(tl.Commits), vcs.MaxTimelineCommits)
	}
}

// TestFetchPRTimelineBestEffort verifica la degradación (§6 F5): patch de un
// commit fallido y discusiones fallidas (500) no fallan la timeline.
func TestFetchPRTimelineBestEffort(t *testing.T) {
	st := &timelineStub{
		commitsJSON:    "[" + glCommitJSON("c1", "2026-10-01T10:00:00Z") + "]",
		commitDiff:     map[string]string{},
		commitDiffCode: map[string]int{},
	}
	st.discussions.code = http.StatusInternalServerError
	ad, repo := newTimelineEnv(t, st)
	slog.SetDefault(slog.New(slog.DiscardHandler))

	tl, err := ad.FetchPRTimeline(context.Background(), repo, timelinePR())
	if err != nil {
		t.Fatalf("fallo best-effort no debe fallar la timeline: %v", err)
	}
	if len(tl.Commits) != 1 || tl.Commits[0].Patch != "" {
		t.Errorf("commit sin patch sobrevive: %+v", tl.Commits)
	}
	if len(tl.Threads) != 0 {
		t.Errorf("threads vacíos ante discussions 500, got %+v", tl.Threads)
	}
}
