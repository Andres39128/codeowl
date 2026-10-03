package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// page de listado: el máximo que acepta la API minimiza las vueltas de
// paginación de ListOpenPRs, FetchPRTimeline y GetDiff.
const page = 100

// glAPIPR es un MR tal como lo devuelve la API de listado.
type glAPIPR struct {
	IID          int64  `json:"iid"`
	Draft        bool   `json:"draft"`
	TargetBranch string `json:"target_branch"`
	Sha          string `json:"sha"` // head del MR
}

// glAPICommit es un commit del MR en la API de commits.
type glAPICommit struct {
	ID         string `json:"id"`
	Message    string `json:"message"`
	AuthorName string `json:"author_name"`
}

// glAPIDiff es un archivo del MR en el endpoint de diffs: GitLab no tiene
// endpoint de diff plano — el campo diff trae solo los hunks.
type glAPIDiff struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	Diff    string `json:"diff"`
}

// glDiscussion es la respuesta de la creación de una discusión.
type glDiscussion struct {
	ID    string `json:"id"`
	Notes []struct {
		ID int64 `json:"id"`
	} `json:"notes"`
}

// glPosition ancla una discusión al diff del MR (mapa: PostInlineComment):
// base/start/head son los diff_refs del MR — el ancla que GitLab exige para
// aceptar la posición (un MR que avanzó rechaza la publicación).
type glPosition struct {
	PositionType string `json:"position_type"` // "text"
	BaseSha      string `json:"base_sha"`
	HeadSha      string `json:"head_sha"`
	StartSha     string `json:"start_sha"`
	OldPath      string `json:"old_path"`
	NewPath      string `json:"new_path"`
	OldLine      *int32 `json:"old_line,omitempty"`
	NewLine      *int32 `json:"new_line,omitempty"`
}

// FetchPR clona shallow el MR al workdir del job (mapa: FetchPR): rama base
// a profundidad 1 + head del MR vía refs/merge-requests/<iid>/head — head, no
// el merge ref: el test-merge contamina el diff y desaparece en conflictos.
// La autenticación es la deploy key SSH del repo (§3.3). merge-base con
// deepen por pasos si falta profundidad (§3.6.1).
func (a *Adapter) FetchPR(ctx context.Context, repo *store.Repository, pr *store.PullRequest, workdir string) error {
	key, err := a.decryptSecret(repo.DeployKey, "deploy_key")
	if err != nil {
		return fmt.Errorf("deploy key para el MR %d: %w", pr.Number, err)
	}
	auth, err := newSSHAuth(key)
	if err != nil {
		return err
	}
	defer auth.cleanup()

	remote := a.sshRemote(repo)
	if err := a.git(ctx, workdir, auth, "clone", "--depth", "1", "--branch", pr.BaseRef, remote, "."); err != nil {
		return fmt.Errorf("clonando base %s: %w", pr.BaseRef, err)
	}
	headRef := fmt.Sprintf("refs/merge-requests/%d/head", pr.Number)
	if err := a.git(ctx, workdir, auth, "fetch", "--depth", "1", "origin", headRef); err != nil {
		return fmt.Errorf("fetch del head del MR %d: %w", pr.Number, err)
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
	key, err := a.decryptSecret(repo.DeployKey, "deploy_key")
	if err != nil {
		return fmt.Errorf("deploy key para clonar %s/%s: %w", repo.Owner, repo.Name, err)
	}
	auth, err := newSSHAuth(key)
	if err != nil {
		return err
	}
	defer auth.cleanup()

	if err := a.git(ctx, workdir, auth, "clone", "--depth", "1", a.sshRemote(repo), "."); err != nil {
		return fmt.Errorf("clonando la rama por defecto: %w", err)
	}
	return nil
}

// FetchPRTimeline trae los commits del MR (mapa: FetchPRTimeline). Para F5 se
// sumará el estado final de las discusiones — devuelve el insumo mínimo.
func (a *Adapter) FetchPRTimeline(ctx context.Context, repo *store.Repository, pr *store.PullRequest) (*vcs.PRTimeline, error) {
	token, err := a.repoToken(repo)
	if err != nil {
		return nil, fmt.Errorf("token para la timeline del MR %d: %w", pr.Number, err)
	}
	timeline := &vcs.PRTimeline{}
	for pageNum := 1; ; pageNum++ {
		path := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/commits?per_page=%d&page=%d",
			a.apiBase(repo), repo.ExternalID, pr.Number, page, pageNum)
		var batch []glAPICommit
		status, err := a.doJSON(ctx, http.MethodGet, path, token, nil, &batch)
		if err != nil {
			return nil, fmt.Errorf("commits del MR %d: %w", pr.Number, err)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("commits del MR %d: status %d", pr.Number, status)
		}
		for _, c := range batch {
			timeline.Commits = append(timeline.Commits, vcs.PRCommit{
				SHA:     c.ID,
				Message: c.Message,
				Author:  c.AuthorName,
			})
		}
		if len(batch) < page {
			return timeline, nil
		}
	}
}

// GetDiff arma el diff unificado base...head del MR (mapa: GetDiff): la API
// de GitLab no expone diff plano — trae los archivos paginados (solo hunks) y
// se ensamblan con encabezados git, el formato que consumen
// vcs.MapFindingToPosition y el detalle de PR del dashboard (F3).
func (a *Adapter) GetDiff(ctx context.Context, repo *store.Repository, pr *store.PullRequest) (string, error) {
	token, err := a.repoToken(repo)
	if err != nil {
		return "", fmt.Errorf("token para el diff del MR %d: %w", pr.Number, err)
	}
	var sb strings.Builder
	for pageNum := 1; ; pageNum++ {
		path := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/diffs?per_page=%d&page=%d",
			a.apiBase(repo), repo.ExternalID, pr.Number, page, pageNum)
		var batch []glAPIDiff
		status, err := a.doJSON(ctx, http.MethodGet, path, token, nil, &batch)
		if err != nil {
			return "", fmt.Errorf("diffs del MR %d: %w", pr.Number, err)
		}
		if status != http.StatusOK {
			return "", fmt.Errorf("diffs del MR %d: status %d", pr.Number, status)
		}
		for _, f := range batch {
			fmt.Fprintf(&sb, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", f.OldPath, f.NewPath, f.OldPath, f.NewPath)
			sb.WriteString(f.Diff)
			if !strings.HasSuffix(f.Diff, "\n") {
				sb.WriteString("\n")
			}
		}
		if len(batch) < page {
			return sb.String(), nil
		}
	}
}

// ListOpenPRs lista los MRs abiertos del repo — insumo del ReconcileJob de la
// reconexión (§3.5). El SHA de la base no viene en el listado: se resuelve el
// tip por rama target vía branchTip (cache §3.6.1.1).
func (a *Adapter) ListOpenPRs(ctx context.Context, repo *store.Repository) ([]vcs.OpenPR, error) {
	token, err := a.repoToken(repo)
	if err != nil {
		return nil, fmt.Errorf("token para listar MRs: %w", err)
	}
	var out []vcs.OpenPR
	for pageNum := 1; ; pageNum++ {
		path := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests?state=opened&per_page=%d&page=%d",
			a.apiBase(repo), repo.ExternalID, page, pageNum)
		var batch []glAPIPR
		status, err := a.doJSON(ctx, http.MethodGet, path, token, nil, &batch)
		if err != nil {
			return nil, fmt.Errorf("listando MRs abiertos: %w", err)
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("listando MRs abiertos: status %d", status)
		}
		for _, p := range batch {
			baseSHA, err := a.branchTip(ctx, repo, p.TargetBranch)
			if err != nil {
				return nil, fmt.Errorf("base del MR %d: %w", p.IID, err)
			}
			out = append(out, vcs.OpenPR{
				Number:  p.IID,
				HeadSHA: p.Sha,
				BaseRef: p.TargetBranch,
				BaseSHA: baseSHA,
				Draft:   p.Draft,
			})
		}
		if len(batch) < page {
			return out, nil
		}
	}
}

// postDiscussion publica en el MR: con posición ancla al diff (discusión
// inline), sin posición crea una nota de conversación. Devuelve el ID del
// hilo/nota creado.
func (a *Adapter) postDiscussion(ctx context.Context, repo *store.Repository, pr *store.PullRequest, body string, pos vcs.CommentPosition) (string, error) {
	token, err := a.repoToken(repo)
	if err != nil {
		return "", fmt.Errorf("token para comentar el MR %d: %w", pr.Number, err)
	}
	reqBody := struct {
		Body     string      `json:"body"`
		Position *glPosition `json:"position,omitempty"`
	}{Body: body}
	if pos.File != "" {
		line := pos.Line
		p := &glPosition{
			PositionType: "text",
			BaseSha:      pr.BaseSha,
			HeadSha:      pr.HeadSha,
			StartSha:     pr.BaseSha, // GitLab ancla el diff base...head en start_sha
			OldPath:      pos.File,
			NewPath:      pos.File,
		}
		if pos.Side == vcs.SideRight {
			p.NewLine = &line // líneas nuevas anclan al lado RIGHT (§3.6.5)
		} else {
			p.OldLine = &line // eliminadas, al LEFT
		}
		reqBody.Position = p
	}
	var out glDiscussion
	path := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/discussions",
		a.apiBase(repo), repo.ExternalID, pr.Number)
	status, err := a.doJSON(ctx, http.MethodPost, path, token, reqBody, &out)
	if err != nil {
		return "", fmt.Errorf("publicando discusión: %w", err)
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("publicando discusión: status %d", status)
	}
	if out.ID == "" {
		return "", fmt.Errorf("publicando discusión: respuesta sin id")
	}
	return out.ID, nil
}

// PostInlineComment publica el comentario del finding en su posición del diff
// (§3.6.5: el mapeo vivió en internal/vcs).
func (a *Adapter) PostInlineComment(ctx context.Context, repo *store.Repository, pr *store.PullRequest, pos vcs.CommentPosition, body string) (string, error) {
	return a.postDiscussion(ctx, repo, pr, body, pos)
}

// PostSuggestion publica el comentario con cambios sugeridos (mapa:
// PostSuggestion). El body ya trae el bloque ```suggestion``` con SOLO el
// código corregido — la cerca oficial de GitLab para "Suggested changes"
// (docs: ```suggestion:-x+y```, la variante sin modificadores reemplaza la
// línea anclada): en una discusión posicionada en el diff, GitLab la
// renderiza con "Apply suggestion". Versiones sin soporte muestran el bloque
// como código plano: el comentario sigue visible, solo pierde el botón.
func (a *Adapter) PostSuggestion(ctx context.Context, repo *store.Repository, pr *store.PullRequest, pos vcs.CommentPosition, body string) (string, error) {
	return a.postDiscussion(ctx, repo, pr, body, pos)
}

// PostSummary publica el resumen único del MR, editado in place en cada
// corrida (§3.6.2): sin commentID crea una nota; con commentID edita vía PUT;
// si la nota fue borrada a mano (404), crea una nueva y devuelve su ID — el
// caller actualiza comments_sent.
func (a *Adapter) PostSummary(ctx context.Context, repo *store.Repository, pr *store.PullRequest, body string, existingCommentID string) (string, error) {
	token, err := a.repoToken(repo)
	if err != nil {
		return "", fmt.Errorf("token para el resumen del MR %d: %w", pr.Number, err)
	}
	if existingCommentID != "" {
		path := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/notes/%s",
			a.apiBase(repo), repo.ExternalID, pr.Number, url.PathEscape(existingCommentID))
		status, err := a.doJSON(ctx, http.MethodPut, path, token,
			map[string]string{"body": body}, nil)
		if err != nil {
			return "", fmt.Errorf("editando resumen %s: %w", existingCommentID, err)
		}
		switch status {
		case http.StatusOK:
			return existingCommentID, nil
		case http.StatusNotFound:
			// Nota borrada a mano: recrear (§3.6.2).
		default:
			return "", fmt.Errorf("editando resumen %s: status %d", existingCommentID, status)
		}
	}
	var out struct {
		ID int64 `json:"id"`
	}
	path := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/notes",
		a.apiBase(repo), repo.ExternalID, pr.Number)
	status, err := a.doJSON(ctx, http.MethodPost, path, token, map[string]string{"body": body}, &out)
	if err != nil {
		return "", fmt.Errorf("creando nota de resumen: %w", err)
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("creando nota de resumen: status %d", status)
	}
	return strconv.FormatInt(out.ID, 10), nil
}

// PostReply responde en el hilo de la nota que disparó el chat (mapa:
// PostReply, F2): las discusiones de GitLab son planas — la respuesta es una
// nota más del MR. El parentCommentID no cambia la publicación: es la unidad
// de idempotencia del caller (comments_sent, §3.3).
func (a *Adapter) PostReply(ctx context.Context, repo *store.Repository, pr *store.PullRequest, parentCommentID, body string) (string, error) {
	token, err := a.repoToken(repo)
	if err != nil {
		return "", fmt.Errorf("token para responder el MR %d: %w", pr.Number, err)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	path := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/notes",
		a.apiBase(repo), repo.ExternalID, pr.Number)
	status, err := a.doJSON(ctx, http.MethodPost, path, token, map[string]string{"body": body}, &out)
	if err != nil {
		return "", fmt.Errorf("creando nota de respuesta: %w", err)
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("creando nota de respuesta: status %d", status)
	}
	return strconv.FormatInt(out.ID, 10), nil
}
