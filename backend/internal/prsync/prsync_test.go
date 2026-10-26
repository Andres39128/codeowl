package prsync

import (
	"testing"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// El sync de PRs (cmd/sync-prs) existe porque la fila de un PR la puebla solo
// el webhook: sin túnel (guía §3.2 desarrollo) los PRs nuevos no llegan al
// sistema. Decide fija en qué casos se encola la revisión.
func TestDecide(t *testing.T) {
	creada := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	abierto := PR{
		Number: 114, Author: "andres", State: "open",
		HeadSha: "head-nuevo", BaseRef: "main", BaseSha: "base-1",
		Created: creada,
	}
	casos := []struct {
		nombre     string
		pr         PR
		known      Known
		draftsOK   bool
		wantReview bool
	}{
		{
			nombre: "PR nuevo: se revisa", pr: abierto,
			known: Known{}, wantReview: true,
		},
		{
			nombre:     "head ya revisado: no se re-encola",
			pr:         abierto,
			known:      Known{Exists: true, HeadSha: "head-nuevo", HasReview: true},
			wantReview: false,
		},
		{
			nombre:     "head conocido sin review: reintento",
			pr:         abierto,
			known:      Known{Exists: true, HeadSha: "head-nuevo", HasReview: false},
			wantReview: true,
		},
		{
			nombre:     "head nuevo: se revisa",
			pr:         abierto,
			known:      Known{Exists: true, HeadSha: "head-viejo", HasReview: true},
			wantReview: true,
		},
		{
			nombre:     "cerrado: no se revisa",
			pr:         PR{Number: 113, State: "closed", HeadSha: "h1", Created: creada},
			known:      Known{Exists: true, HeadSha: "h1", HasReview: true},
			wantReview: false,
		},
		{
			nombre:   "draft sin review_drafts: no se revisa",
			pr:       PR{Number: 115, State: "open", Draft: true, HeadSha: "h2", Created: creada},
			known:    Known{},
			draftsOK: false, wantReview: false,
		},
		{
			nombre:   "draft con review_drafts: se revisa",
			pr:       PR{Number: 115, State: "open", Draft: true, HeadSha: "h2", Created: creada},
			known:    Known{},
			draftsOK: true, wantReview: true,
		},
	}
	for _, c := range casos {
		got := Decide(c.pr, c.known, c.draftsOK)
		if got.Review != c.wantReview {
			t.Errorf("%s: want review=%v, got %v (%s)", c.nombre, c.wantReview, got.Review, got.Reason)
		}
		if got.Reason == "" {
			t.Errorf("%s: la decisión debe explicar el motivo", c.nombre)
		}
	}
}

// FromOpenPR proyecta el PR del VCS al dominio del sync: el listado del VCS
// solo trae PRs abiertos, así que el estado es "open" y no hay merge.
func TestFromOpenPR(t *testing.T) {
	creada := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	got := FromOpenPR(vcs.OpenPR{
		Number: 114, HeadSHA: "head-1", BaseRef: "main", BaseSHA: "base-1",
		Draft: false, Author: "andres", CreatedAt: creada,
	})
	if got.Number != 114 || got.State != "open" || got.Author != "andres" {
		t.Errorf("proyección incompleta: %+v", got)
	}
	if got.HeadSha != "head-1" || got.BaseRef != "main" || got.BaseSha != "base-1" {
		t.Errorf("proyección de shas incompleta: %+v", got)
	}
	if !got.Created.Equal(creada) {
		t.Errorf("created_at: got %v want %v", got.Created, creada)
	}
	if got.MergedAt != nil {
		t.Error("un PR abierto no tiene merge")
	}
}
