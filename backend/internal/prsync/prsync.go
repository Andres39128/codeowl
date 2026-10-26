package prsync

import (
	"fmt"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// PR es la proyección mínima de un PR del VCS para el sync: los mismos
// campos que el webhook usa para upsert y encolar (author, state, head/base
// sha, draft, merge).
type PR struct {
	Number   int64      `json:"number"`
	Author   string     `json:"author"`
	State    string     `json:"state"`
	Draft    bool       `json:"draft"`
	HeadSha  string     `json:"head_sha"`
	BaseRef  string     `json:"base_ref"`
	BaseSha  string     `json:"base_sha"`
	MergedAt *time.Time `json:"merged_at"`
	Created  time.Time  `json:"created_at"`
}

// Known es lo que el sistema ya sabe de un PR: la fila (si existe) con su
// head_sha conocido y si tiene alguna review.
type Known struct {
	Exists    bool
	HeadSha   string
	HasReview bool
}

// Decision dice si el PR necesita una revisión nueva y por qué.
type Decision struct {
	Review bool
	Reason string
}

// Decide cuándo encolar la revisión de un PR durante el sync. Reglas:
//  1. PR cerrado/mergeado: no se revisa (el webhook de "closed" ya.corrió su
//     propio camino; acá solo se actualiza la fila).
//  2. Draft sin review_drafts del repo: no se revisa (§3.5, mismo corte que
//     el webhook).
//  3. head_sha igual al conocido y con review previa: ya se revisó ese
//     contenido — no se re-encola (idempotencia del sync, como el
//     "synchronize" del webhook solo dispara con head nuevo).
//  4. head_sha igual al conocido pero SIN review: se re-encola (la review
//     anterior pudo fallar o quedar stale sin reintento).
//  5. head distinto o PR nuevo: se revisa.
func Decide(pr PR, known Known, reviewDrafts bool) Decision {
	if pr.State != "open" {
		return Decision{false, fmt.Sprintf("PR cerrado (state=%s): no se revisa", pr.State)}
	}
	if pr.Draft && !reviewDrafts {
		return Decision{false, "draft sin review (config del repo)"}
	}
	if known.Exists && known.HeadSha == pr.HeadSha {
		if known.HasReview {
			return Decision{false, "head ya revisado"}
		}
		return Decision{true, "head conocido sin review: reintento"}
	}
	if known.Exists {
		return Decision{true, "head nuevo: se revisa"}
	}
	return Decision{true, "PR nuevo: se revisa"}
}

// FromOpenPR proyecta un PR abierto del VCS al dominio del sync: el listado
// del VCS solo trae PRs abiertos, así que el estado es "open" por definición
// y no hay merge (MergedAt nil).
func FromOpenPR(p vcs.OpenPR) PR {
	return PR{
		Number:  p.Number,
		Author:  p.Author,
		State:   "open",
		Draft:   p.Draft,
		HeadSha: p.HeadSHA,
		BaseRef: p.BaseRef,
		BaseSha: p.BaseSHA,
		Created: p.CreatedAt,
	}
}
