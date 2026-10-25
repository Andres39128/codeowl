package backfill

import (
	"testing"
	"time"
)

// El backfill reconstruye pushes directos que el webhook nunca vio: commits
// en la rama base que NO provienen de un PR mergeado (S4/T1 de
// trazabilidad-merges). La regla (§ detection): se excluyen el root commit,
// los merges de PR por web ("Merge pull request ..."), cualquier sha presente
// en mergedPRSHAs, y se conserva el orden de entrada. Cada candidato lleva
// before = primer padre, after = su sha.
func TestDetectaCandidatosSinPR(t *testing.T) {
	dia := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }
	commits := []Commit{
		{
			SHA: "aaaa0001", Parents: []string{},
			Message: "initial commit", Committer: "andres", Date: dia(1),
		},
		{
			SHA: "bbbb0002", Parents: []string{"aaaa0001"},
			Message: "feat: directo sin PR", Committer: "andres", Date: dia(2),
		},
		{
			SHA: "cccc0003", Parents: []string{"bbbb0002", "ffff0009"},
			Message:   "Merge pull request #12 from Andres39128/fix/x",
			Committer: "andres", Date: dia(3),
		},
		{
			SHA: "dddd0004", Parents: []string{"cccc0003", "eeee0008"},
			Message:   "Merge fix/y: merge local sin PR",
			Committer: "andres", Date: dia(4),
		},
	}
	mergedPRSHAs := map[string]bool{"cccc0003": true, "9999zzz9": true}

	got := Candidates(commits, mergedPRSHAs)

	// Root fuera; merge de PR fuera (doble corte: mensaje y mergedPRSHAs);
	// entran el commit lineal directo y el merge local.
	if len(got) != 2 {
		t.Fatalf("esperaba 2 candidatos (lineal + merge local), got %d: %+v", len(got), got)
	}
	if got[0].SHA != "bbbb0002" || got[1].SHA != "dddd0004" {
		t.Errorf("orden de entrada debe conservarse: %v %v", got[0].SHA, got[1].SHA)
	}
	if got[0].Before != "aaaa0001" || got[0].After != "bbbb0002" {
		t.Errorf("before/after del commit lineal: %s→%s", got[0].Before, got[0].After)
	}
	if got[1].Before != "cccc0003" || got[1].After != "dddd0004" {
		t.Errorf("before/after del merge local (primer padre): %s→%s", got[1].Before, got[1].After)
	}
}

// El corte por mensaje es prefijo-insensible a mayúsculas y espacios, y exige
// el formato de merge de PR de GitHub; otros mensajes de merge no cortan.
func TestCorteMensajeMergeDePR(t *testing.T) {
	d0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	nuevo := func(sha, msg string) Commit {
		return Commit{SHA: sha, Parents: []string{"p"}, Message: msg, Committer: "a", Date: d0}
	}
	casos := []struct {
		msg      string
		incluido bool
	}{
		{"Merge pull request #113 from Andres39128/develop", false},
		{"merge pull request #5 from x/y", false},
		{"  Merge pull request #7 from a/b", false},
		{"Merge fix/y: local", true},
		{"Merge remote-tracking branch 'origin/main'", true},
		{"Merge origin/main (PR #113 develop) into folio", true},
		{"feat: squash de PR (sin mensaje de merge)", true},
	}
	for _, c := range casos {
		got := Candidates([]Commit{nuevo("s1", c.msg)}, nil)
		if (len(got) == 1) != c.incluido {
			t.Errorf("msg %q: want incluido=%v, got %d candidatos", c.msg, c.incluido, len(got))
		}
	}
}

// IsMerge distingue merges (≥2 padres) de lineales y root: el backfill solo
// reconstruye merges sin PR (-merges-only, S1); los commits lineales directos
// a la base los captura el webhook en vivo, y backfillar todos multiplicaría
// los análisis sin valor.
func TestIsMerge(t *testing.T) {
	merge := Commit{SHA: "m1", Parents: []string{"a", "b"}}
	lineal := Commit{SHA: "l1", Parents: []string{"a"}}
	root := Commit{SHA: "r1"}
	if !merge.IsMerge() {
		t.Error("commit con 2 padres debe ser merge")
	}
	if lineal.IsMerge() || root.IsMerge() {
		t.Error("lineal y root no son merges")
	}
}
