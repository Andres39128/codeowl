package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// page de listado: el máximo que acepta la API minimiza las vueltas de
// paginación de ListOpenPRs y FetchPRTimeline.
const page = 100

// ghAPIPR es el PR tal como lo devuelve la API de listado.
type ghAPIPR struct {
	Number int64  `json:"number"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	Head   ghRef  `json:"head"`
	Base   ghRef  `json:"base"`
}

// ghAPICommit es un commit del PR en la API de timeline.
type ghAPICommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"commit"`
}

// FetchPR clona shallow el PR al workdir del job (mapa: FetchPR): rama base
// a profundidad 1 + head del PR vía refs/pull/<n>/head del repo base — el
// ref vive en el repo base también para forks (el head de un fork externo no
// es accesible con el token de instalación). Si git merge-base falla por
// falta de profundidad, deepen y reintenta.
func (a *Adapter) FetchPR(ctx context.Context, repo *store.Repository, pr *store.PullRequest, workdir string) error {
	token, err := a.repoToken(ctx, repo)
	if err != nil {
		return fmt.Errorf("token de instalación para el PR %d: %w", pr.Number, err)
	}
	auth, err := newGitAuth(token)
	if err != nil {
		return err
	}
	defer auth.cleanup()

	remote := fmt.Sprintf("https://github.com/%s/%s.git", repo.Owner, repo.Name)
	if err := a.git(ctx, workdir, auth, "clone", "--depth", "1", "--branch", pr.BaseRef, remote, "."); err != nil {
		return fmt.Errorf("clonando base %s: %w", pr.BaseRef, err)
	}
	headRef := fmt.Sprintf("refs/pull/%d/head", pr.Number)
	if err := a.git(ctx, workdir, auth, "fetch", "--depth", "1", "origin", headRef); err != nil {
		return fmt.Errorf("fetch del head del PR %d: %w", pr.Number, err)
	}

	// merge-base contra el ancla de la base (§3.6.1.1): con shallow falta
	// historia — deepen por pasos hasta encontrarla o rendirse (el job
	// reintenta y el error queda registrado).
	const deepenStep, maxDeepens = 256, 4
	for attempt := 0; ; attempt++ {
		if err := a.git(ctx, workdir, auth, "merge-base", pr.BaseSha, pr.HeadSha); err == nil {
			return nil
		}
		if attempt >= maxDeepens {
			return fmt.Errorf("merge-base %s...%s sin profundidad suficiente tras %d deepen", pr.BaseSha, pr.HeadSha, maxDeepens)
		}
		for _, ref := range []string{pr.BaseRef, headRef} {
			if err := a.git(ctx, workdir, auth, "fetch", "--deepen", strconv.Itoa(deepenStep), "origin", ref); err != nil {
				return fmt.Errorf("deepen de %s: %w", ref, err)
			}
		}
	}
}

// FetchDefaultBranch clona shallow la rama por defecto al workdir del job —
// insumo del IndexJob (mapa: FetchDefaultBranch). Un clone sin --branch trae
// el HEAD del remote: la default branch, sin llamada API extra.
func (a *Adapter) FetchDefaultBranch(ctx context.Context, repo *store.Repository, workdir string) error {
	token, err := a.repoToken(ctx, repo)
	if err != nil {
		return fmt.Errorf("token de instalación para clonar %s/%s: %w", repo.Owner, repo.Name, err)
	}
	auth, err := newGitAuth(token)
	if err != nil {
		return err
	}
	defer auth.cleanup()

	remote := fmt.Sprintf("https://github.com/%s/%s.git", repo.Owner, repo.Name)
	if err := a.git(ctx, workdir, auth, "clone", "--depth", "1", remote, "."); err != nil {
		return fmt.Errorf("clonando la rama por defecto: %w", err)
	}
	return nil
}

// FetchPRTimeline trae los commits del PR (mapa: FetchPRTimeline). Para F5
// se sumará el estado final de los threads — F1 devuelve el insumo mínimo.
func (a *Adapter) FetchPRTimeline(ctx context.Context, repo *store.Repository, pr *store.PullRequest) (*vcs.PRTimeline, error) {
	token, err := a.repoToken(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("token de instalación para la timeline del PR %d: %w", pr.Number, err)
	}
	timeline := &vcs.PRTimeline{}
	for pageNum := 1; ; pageNum++ {
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d/commits?per_page=%d&page=%d",
			repo.Owner, repo.Name, pr.Number, page, pageNum)
		var batch []ghAPICommit
		status, err := a.doJSON(ctx, http.MethodGet, path, token, mediaTypeJSON, nil, &batch)
		if err != nil {
			return nil, fmt.Errorf("commits del PR %d: %w", pr.Number, err)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("commits del PR %d: status %d", pr.Number, status)
		}
		for _, c := range batch {
			timeline.Commits = append(timeline.Commits, vcs.PRCommit{
				SHA:     c.SHA,
				Message: c.Commit.Message,
				Author:  c.Commit.Author.Name,
			})
		}
		if len(batch) < page {
			return timeline, nil
		}
	}
}

// GetDiff produce el diff unificado base...head vía el endpoint compare de
// la API con media type de diff — tres puntos: diff contra el merge-base,
// exactamente el insumo del pipeline (§3.6). El adapter no conoce el workdir
// del job (no viaja en el contrato), así que no hay rama local-clone.
func (a *Adapter) GetDiff(ctx context.Context, repo *store.Repository, pr *store.PullRequest) (string, error) {
	token, err := a.repoToken(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("token de instalación para el diff del PR %d: %w", pr.Number, err)
	}
	req, err := newAPIRequest(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/%s/compare/%s...%s", repo.Owner, repo.Name, pr.BaseSha, pr.HeadSha),
		token, mediaTypeDiff, nil)
	if err != nil {
		return "", err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("comparando %s...%s: %w", pr.BaseSha, pr.HeadSha, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", apiError(http.MethodGet, req.URL.Path, resp.StatusCode, resp.Body)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("leyendo diff del PR %d: %w", pr.Number, err)
	}
	return string(b), nil
}

// ListOpenPRs lista los PRs abiertos del repo — insumo del ReconcileJob de
// la reconexión (§3.5). Pagina hasta agotar.
func (a *Adapter) ListOpenPRs(ctx context.Context, repo *store.Repository) ([]vcs.OpenPR, error) {
	token, err := a.repoToken(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("token de instalación para listar PRs: %w", err)
	}
	var out []vcs.OpenPR
	for pageNum := 1; ; pageNum++ {
		path := fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=%d&page=%d",
			repo.Owner, repo.Name, page, pageNum)
		var batch []ghAPIPR
		status, err := a.doJSON(ctx, http.MethodGet, path, token, mediaTypeJSON, nil, &batch)
		if err != nil {
			return nil, fmt.Errorf("listando PRs abiertos: %w", err)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("listando PRs abiertos: status %d", status)
		}
		for _, p := range batch {
			out = append(out, vcs.OpenPR{
				Number:  p.Number,
				HeadSHA: p.Head.Sha,
				BaseRef: p.Base.Ref,
				BaseSHA: p.Base.Sha,
				Draft:   p.Draft,
			})
		}
		if len(batch) < page {
			return out, nil
		}
	}
}

// postPRComment publica un comentario inline sobre el diff del PR con
// posición (side/line) anclada al head (mapa: PostInlineComment).
func (a *Adapter) postPRComment(ctx context.Context, repo *store.Repository, pr *store.PullRequest, body string, pos vcs.CommentPosition) (string, error) {
	token, err := a.repoToken(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("token de instalación para comentar el PR %d: %w", pr.Number, err)
	}
	reqBody := struct {
		Body     string `json:"body"`
		CommitID string `json:"commit_id"`
		Path     string `json:"path"`
		Line     int32  `json:"line"`
		Side     string `json:"side"`
	}{
		Body:     body,
		CommitID: pr.HeadSha,
		Path:     pos.File,
		Line:     pos.Line,
		Side:     string(pos.Side),
	}
	var out struct {
		ID int64 `json:"id"`
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/comments", repo.Owner, repo.Name, pr.Number)
	status, err := a.doJSON(ctx, http.MethodPost, path, token, mediaTypeJSON, reqBody, &out)
	if err != nil {
		return "", fmt.Errorf("publicando comentario inline: %w", err)
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("publicando comentario inline: status %d", status)
	}
	return strconv.FormatInt(out.ID, 10), nil
}

// PostInlineComment publica el comentario del finding en su posición del
// diff (§3.6.5: el mapeo vivió en internal/vcs).
func (a *Adapter) PostInlineComment(ctx context.Context, repo *store.Repository, pr *store.PullRequest, pos vcs.CommentPosition, body string) (string, error) {
	return a.postPRComment(ctx, repo, pr, body, pos)
}

// PostSuggestion publica la sugerencia como bloque ```suggestion``` —
// aplicable con un clic desde GitHub (mapa: PostSuggestion).
func (a *Adapter) PostSuggestion(ctx context.Context, repo *store.Repository, pr *store.PullRequest, pos vcs.CommentPosition, body string) (string, error) {
	wrapped := "```suggestion\n" + body + "\n```"
	return a.postPRComment(ctx, repo, pr, wrapped, pos)
}

// PostSummary publica el resumen único del PR, editado in place en cada
// corrida (§3.6.2): sin commentID crea; con commentID edita vía PATCH; si el
// comentario fue borrado a mano (404), crea uno nuevo y devuelve su ID —
// el caller actualiza comments_sent.
func (a *Adapter) PostSummary(ctx context.Context, repo *store.Repository, pr *store.PullRequest, body string, existingCommentID string) (string, error) {
	token, err := a.repoToken(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("token de instalación para el resumen del PR %d: %w", pr.Number, err)
	}
	if existingCommentID != "" {
		path := fmt.Sprintf("/repos/%s/%s/issues/comments/%s", repo.Owner, repo.Name, existingCommentID)
		status, err := a.doJSON(ctx, http.MethodPatch, path, token, mediaTypeJSON,
			map[string]string{"body": body}, nil)
		if err != nil {
			return "", fmt.Errorf("editando resumen %s: %w", existingCommentID, err)
		}
		switch status {
		case http.StatusOK:
			return existingCommentID, nil
		case http.StatusNotFound:
			// Comentario borrado a mano: recrear (§3.6.2).
		default:
			return "", fmt.Errorf("editando resumen %s: status %d", existingCommentID, status)
		}
	}
	return a.postIssueComment(ctx, repo, pr.Number, token, body)
}

// postIssueComment crea un comentario en la conversación del PR (la API de
// issues es la superficie de GitHub para comentarios de conversación).
func (a *Adapter) postIssueComment(ctx context.Context, repo *store.Repository, number int64, token, body string) (string, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", repo.Owner, repo.Name, number)
	status, err := a.doJSON(ctx, http.MethodPost, path, token, mediaTypeJSON,
		map[string]string{"body": body}, &out)
	if err != nil {
		return "", fmt.Errorf("creando comentario de resumen: %w", err)
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("creando comentario de resumen: status %d", status)
	}
	return strconv.FormatInt(out.ID, 10), nil
}

// doJSON arma la request, la ejecuta y decodea la respuesta JSON en out.
func (a *Adapter) doJSON(ctx context.Context, method, path, token, accept string, body, out any) (int, error) {
	req, err := newAPIRequest(ctx, method, path, token, accept, body)
	if err != nil {
		return 0, err
	}
	status, err := a.do(ctx, req, out)
	if err != nil {
		return 0, err
	}
	return status, nil
}
