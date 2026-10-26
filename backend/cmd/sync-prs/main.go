// Command sync-prs trae a codeowl los PRs que el webhook no trajo: sin túnel
// de webhooks (guía §3.2, desarrollo) los PRs nuevos no llegan al sistema y
// el dashboard no los ve. Es el mismo camino que el webhook para un PR
// (upsert de la fila + encolado del ReviewJob), con la diferencia de que el
// sync es idempotente: un PR cuyo head_sha ya se revisó no se re-encola
// (mismo corte de identidad que el "synchronize" del webhook).
//
// Uso one-shot; el fetch es del operador:
//
//	gh api "repos/OWNER/REPO/pulls?state=all&per_page=100" \
//	  --jq '[.[] | {number, author: .user.login, state, draft, head_sha: .head.sha, base_ref: .base.ref, base_sha: .base.sha, merged_at, created_at}]' \
//	  > /tmp/prs.json
//	just sync-prs -repo-id 943 -prs /tmp/prs.json          # dry-run
//	just sync-prs -repo-id 943 -prs /tmp/prs.json -apply
//
// El secret del webhook no se toca: el sync no publica nada en el VCS.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/prsync"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func main() {
	if err := run(); err != nil {
		slog.Error("sync-prs", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	repoID := flag.Int64("repo-id", 0, "id del repo en codeowl (obligatorio)")
	prsPath := flag.String("prs", "", "JSON de PRs del repo (obligatorio)")
	apply := flag.Bool("apply", false, "insertar y encolar (default: dry-run)")
	flag.Parse()

	if *repoID == 0 || *prsPath == "" {
		flag.Usage()
		return fmt.Errorf("faltan flags obligatorios")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("DATABASE_URL no está seteado (just carga el .env)")
	}

	raw, err := os.ReadFile(*prsPath)
	if err != nil {
		return fmt.Errorf("leyendo %s: %w", *prsPath, err)
	}
	var prs []prsync.PR
	if err := json.Unmarshal(raw, &prs); err != nil {
		return fmt.Errorf("parseando %s: %w", *prsPath, err)
	}

	st, err := store.Open(ctx, url)
	if err != nil {
		return fmt.Errorf("abriendo la store: %w", err)
	}
	defer st.Close()

	repo, err := st.GetRepository(ctx, *repoID)
	if err != nil {
		return fmt.Errorf("cargando el repo %d: %w", *repoID, err)
	}

	var q *jobs.RiverQueue
	if *apply {
		q, err = jobs.New(ctx, st.Pool)
		if err != nil {
			return fmt.Errorf("abriendo la cola: %w", err)
		}
	}

	encolados := 0
	for _, pr := range prs {
		known := prsync.Known{}
		fila, err := st.GetPullRequestByRepoNumber(ctx, store.GetPullRequestByRepoNumberParams{
			RepositoryID: repo.ID, Number: pr.Number,
		})
		switch {
		case err == nil:
			known.Exists = true
			known.HeadSha = fila.HeadSha
			review, err := st.GetLatestReviewByPR(ctx, fila.ID)
			if err == nil {
				known.HasReview = true
				_ = review
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("leyendo la última review del PR %d: %w", pr.Number, err)
			}
		case errors.Is(err, pgx.ErrNoRows):
			// PR nuevo: no hay fila todavía.
		default:
			return fmt.Errorf("buscando el PR %d: %w", pr.Number, err)
		}

		d := prsync.Decide(pr, known, repo.ReviewDrafts)
		_, _ = fmt.Fprintf(os.Stdout, "#%d %s %s head=%s → review=%v (%s)\n",
			pr.Number, pr.State, pr.Author, short(pr.HeadSha), d.Review, d.Reason)
		if !d.Review {
			continue
		}
		if !*apply {
			continue
		}

		upserted, err := st.UpsertPullRequest(ctx, store.UpsertPullRequestParams{
			RepositoryID: repo.ID,
			Number:       pr.Number,
			Author:       pr.Author,
			State:        pr.State,
			HeadSha:      pr.HeadSha,
			BaseRef:      pr.BaseRef,
			BaseSha:      pr.BaseSha,
			MergedAt:     mergedAt(pr.MergedAt),
			CreatedAt:    createdAt(pr.Created),
		})
		if err != nil {
			return fmt.Errorf("upsert del PR %d: %w", pr.Number, err)
		}
		args, err := json.Marshal(jobs.ReviewJobArgs{
			RepositoryID:  repo.ID,
			PullRequestID: upserted.ID,
			HeadSha:       pr.HeadSha,
			BaseSha:       pr.BaseSha,
		})
		if err != nil {
			return fmt.Errorf("serializando args del PR %d: %w", pr.Number, err)
		}
		if err := q.Enqueue(ctx, jobs.KindReview, args); err != nil {
			return fmt.Errorf("encolando el ReviewJob del PR %d: %w", pr.Number, err)
		}
		encolados++
	}
	if !*apply {
		_, _ = fmt.Fprintln(os.Stdout, "dry-run: sin -apply no se escribe nada")
		return nil
	}
	slog.Info("sync-prs completo", "repo", repo.Owner+"/"+repo.Name, "prs", len(prs), "encolados", encolados)
	return nil
}

// short recorta el sha para el listado.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// mergedAt convierte la fecha opcional de merge a pgtype (NULL si no hubo).
func mergedAt(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// createdAt convierte la fecha de creación a pgtype (NULL si no vino).
func createdAt(t time.Time) pgtype.Timestamptz {
	if t.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: t, Valid: true}
}
