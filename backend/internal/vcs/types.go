// Package vcs es el contrato VCSProvider y los helpers compartidos entre
// adapters (mapa: backend.vcs). Agregar un VCS nuevo = paquete nuevo que
// cumpla la interfaz: prohibido que el resto del código sepa qué proveedor
// está activo.
package vcs

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
}

// PRTimeline agrupa los commits del PR y — desde F5 — el estado final de sus
// threads de review: insumo del MetricsJob (mapa: FetchPRTimeline).
type PRTimeline struct {
	Commits []PRCommit
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
