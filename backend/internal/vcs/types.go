// Package vcs es el contrato VCSProvider y los helpers compartidos entre
// adapters (mapa: backend.vcs). Agregar un VCS nuevo = paquete nuevo que
// cumpla la interfaz: prohibido que el resto del código sepa qué proveedor
// está activo.
package vcs

import "time"

// Topes del contrato FetchPRTimeline (§6 F5): el insumo del MetricsJob es
// heurístico — primeros 50 commits con patch acotado, no historia completa.
const (
	MaxTimelineCommits = 50      // commits por timeline
	MaxPatchBytes      = 64 << 10 // patch por commit, truncado a 64KB
)

// Side identifica a qué lado del diff ancla un comentario inline (§3.6.5):
// RIGHT para líneas nuevas, LEFT para líneas eliminadas.
type Side string

const (
	SideRight Side = "RIGHT"
	SideLeft  Side = "LEFT"
)

// CommentPosition es la posición de un comentario inline en el VCS,
// reconciliada contra los hunks del diff (§3.6.5). El mapeo
// finding → posición vive solo en internal/vcs.
type CommentPosition struct {
	File string
	Line int32
	Side Side
}

// PRCommit es un commit del PR, insumo del FetchPRTimeline.
type PRCommit struct {
	SHA     string
	Message string
	Author  string
	// AuthoredAt es la fecha de autoría del commit (author date, no la de
	// committer). Cero si el proveedor no la informa: el campo es best-effort.
	AuthoredAt time.Time
	// Patch es el diff unificado del commit, truncado a MaxPatchBytes.
	// Vacío si el fetch del patch falló (best-effort: un patch ausente no
	// falla la timeline — el commit sigue siendo insumo válido).
	Patch string
}

// PRThread es el estado final de un hilo de review inline del PR: insumo de
// la tasa de falsos positivos del MetricsJob (§6 F5).
type PRThread struct {
	// CommentID es el MISMO identificador que el adapter devuelve al
	// publicar (PostInlineComment/PostSuggestion) — la clave contra
	// comments_sent.comment_id: GitHub = databaseId del comentario de
	// review REST; GitLab = id de la discusión.
	CommentID string
	// Resolved dice si el hilo terminó resuelto. GitLab reporta la
	// resolución por nota solo si el proyecto la tiene habilitada: si la
	// flag no viene, es false honesto (el adapter lo registra en debug).
	Resolved bool
}

// PRTimeline agrupa los commits del PR y — desde F5 — el estado final de sus
// threads de review: insumo del MetricsJob (mapa: FetchPRTimeline). Semántica
// best-effort (§6 F5): commits acotados a MaxTimelineCommits; Threads es
// vacío (sin error) si el fetch de threads falla — solo el fallo del
// endpoint de commits se devuelve como error.
type PRTimeline struct {
	Commits []PRCommit
	Threads []PRThread
}

// OpenPR es un PR abierto del repo según el VCS: insumo del ReconcileJob de
// la reconexión (§3.5) — los cierres durante la desconexión no llegan como
// evento y se marcan cerrados por comparación contra esta lista.
type OpenPR struct {
	Number  int64
	HeadSHA string
	BaseRef string
	BaseSHA string
	Draft   bool
}
