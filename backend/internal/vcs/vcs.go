package vcs

import (
	"bufio"
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// VCSProvider es el contrato por proveedor (mapa: backend.vcs). Cada método
// tiene semántica definida en el mapa: HandleWebhook valida, filtra y
// encola — jamás llama LLM ni ejecuta análisis (§3.5); FetchPR clona shallow
// el PR al workdir del job; FetchDefaultBranch clona la rama por defecto;
// FetchPRTimeline trae commits (y en F5, threads); GetDiff produce el diff
// unificado bajo demanda; ListOpenPRs alimenta la reconciliación de estado;
// Post* publican comentarios (inline con posición, sugerencia aplicable y
// resumen editado in place, §3.6) y la respuesta de chat en el hilo del
// comentario que disparó la mención (F2).
type VCSProvider interface {
	HandleWebhook(ctx context.Context, w http.ResponseWriter, r *http.Request)
	FetchPR(ctx context.Context, repo *store.Repository, pr *store.PullRequest, workdir string) error
	FetchDefaultBranch(ctx context.Context, repo *store.Repository, workdir string) error
	FetchPRTimeline(ctx context.Context, repo *store.Repository, pr *store.PullRequest) (*PRTimeline, error)
	GetDiff(ctx context.Context, repo *store.Repository, pr *store.PullRequest) (string, error)
	ListOpenPRs(ctx context.Context, repo *store.Repository) ([]OpenPR, error)
	PostInlineComment(ctx context.Context, repo *store.Repository, pr *store.PullRequest, pos CommentPosition, body string) (commentID string, err error)
	PostSuggestion(ctx context.Context, repo *store.Repository, pr *store.PullRequest, pos CommentPosition, body string) (commentID string, err error)
	PostSummary(ctx context.Context, repo *store.Repository, pr *store.PullRequest, body string, existingCommentID string) (commentID string, err error)
	PostReply(ctx context.Context, repo *store.Repository, pr *store.PullRequest, parentCommentID, body string) (commentID string, err error)
}

// hunkHeaderRe matchea el encabezado de hunk del diff unificado, con conteos
// colapsados incluidos ("@@ -1 +1 @@" omite ",1" — contexto colapsado).
var hunkHeaderRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// MapFindingToPosition convierte finding.file/line en CommentPosition
// reconciliando contra los hunks del diff unificado (§3.6.5): las líneas que
// existen en el lado nuevo (agregadas o de contexto) anclan al lado RIGHT;
// un hallazgo sobre una línea que solo existe del lado viejo — un cleanup
// borrado, una validación quitada — ancla al LEFT del hunk. Si la línea
// quedó fuera del diff visible devuelve ok=false: el hallazgo baja al
// resumen. RIGHT tiene precedencia: el finding describe el código del head.
func MapFindingToPosition(diff, file string, line int32) (CommentPosition, bool) {
	if pos, ok := scanForSide(diff, file, line, SideRight); ok {
		return pos, true
	}
	return scanForSide(diff, file, line, SideLeft)
}

// scanForSide recorre los hunks del archivo buscando la línea anclable al
// lado dado: RIGHT matchea líneas agregadas y de contexto (existen en el
// archivo nuevo); LEFT, solo eliminadas. Los contadores avanzan con las
// líneas de contenido — los conteos del encabezado no se confían.
func scanForSide(diff, file string, line int32, side Side) (CommentPosition, bool) {
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	inFile := false
	var oldLine, newLine int32
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "diff --git "):
			inFile = false // nueva sección: la ruta se confirma con +++
		case strings.HasPrefix(l, "+++ "):
			path := strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
			inFile = path == file
		case strings.HasPrefix(l, "@@ "):
			if !inFile {
				continue
			}
			m := hunkHeaderRe.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			oldLine = mustAtoI32(m[1])
			newLine = mustAtoI32(m[3])
		case inFile && strings.HasPrefix(l, "+"):
			if side == SideRight && line == newLine {
				return CommentPosition{File: file, Line: line, Side: SideRight}, true
			}
			newLine++
		case inFile && strings.HasPrefix(l, "-"):
			if side == SideLeft && line == oldLine {
				return CommentPosition{File: file, Line: line, Side: SideLeft}, true
			}
			oldLine++
		case inFile && (strings.HasPrefix(l, " ") || l == ""):
			if side == SideRight && line == newLine {
				return CommentPosition{File: file, Line: line, Side: SideRight}, true
			}
			oldLine++
			newLine++
		}
		// "\ No newline at end of file" y encabezados del archivo no mueven
		// los contadores.
	}
	return CommentPosition{}, false
}

// mustAtoI32 parsea un número de línea del encabezado de hunk (siempre
// dígitos: el error es imposible; default 0 si excede int32 — el hunk no
// va a matchear una línea inalcanzable).
func mustAtoI32(s string) int32 {
	n, _ := strconv.ParseInt(s, 10, 64)
	if n < -1<<31 || n > 1<<31-1 {
		return 0
	}
	return int32(n)
}
