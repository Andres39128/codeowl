// Package backfill reconstruye pushes directos históricos — merges sin PR y
// commits directos a la rama base — que el webhook nunca vio: S4 de
// trazabilidad-merges solo registra pushes en vivo, sin backfill.
// La detección (Candidates) es pura; main.go la alimenta con el listado de
// commits de la rama base y los shas de PRs mergeados (fetch con gh api).
package backfill

import (
	"strings"
	"time"
)

// Commit es la proyección mínima de un commit de la rama base para la
// detección; Before/After los completa Candidates (par del push directo,
// igual semántica que el before/after del payload del webhook).
type Commit struct {
	SHA       string    `json:"sha"`
	Parents   []string  `json:"parents"`
	Message   string    `json:"message"`
	Committer string    `json:"committer"`
	Date      time.Time `json:"date"`
	Before    string    `json:"-"`
	After     string    `json:"-"`
}

// prMergePrefix es el formato del mensaje de merge por web de GitHub (todo
// merge-commit de PR lo trae). Cinturón y tirantes: los merges de PR también
// se excluyen por sha vía mergedPRSHAs.
const prMergePrefix = "merge pull request"

// Candidates devuelve los commits de la rama base que no provienen de un PR
// mergeado: cada uno es un push directo que el webhook habría registrado.
// Se excluyen el root commit (sin padres, no hay push que reconstruir), los
// shas de PRs mergeados conocidos y los merges por web. El orden de entrada
// se conserva (el cmd lo ordena viejo→nuevo antes de insertar).
func Candidates(commits []Commit, mergedPRSHAs map[string]bool) []Commit {
	out := make([]Commit, 0, len(commits))
	for _, c := range commits {
		if len(c.Parents) == 0 {
			continue
		}
		if mergedPRSHAs[c.SHA] {
			continue
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.Message)), prMergePrefix) {
			continue
		}
		c.Before = c.Parents[0]
		c.After = c.SHA
		out = append(out, c)
	}
	return out
}

// IsMerge informa si el commit es un merge (≥2 padres). El backfill lo usa
// para acotarse a merges sin PR (S1 de trazabilidad-merges): los commits
// lineales directos los captura el webhook en vivo, y backfillarlos a todos
// multiplicaría los análisis sin valor.
func (c Commit) IsMerge() bool {
	return len(c.Parents) >= 2
}
