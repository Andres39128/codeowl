// Command backfill-pushes reconstruye pushes directos históricos de UN repo —
// merges sin PR y commits directos a la rama base — que el webhook nunca vio:
// S4 de trazabilidad-merges solo registra pushes en vivo (sin backfill).
// Reusa la MISMA ingesta del webhook: UpsertDirectPush (idempotente por
// (repository_id, after_sha)) + encolado de PushReviewJob.
//
// Uso one-shot (guía §9.13 local); el fetch es responsabilidad del operador:
//
//	set -a; . ./.env; set +a
//	gh api "repos/OWNER/REPO/commits?sha=BRANCH&since=ISO8601&per_page=100" --paginate \
//	  --jq 'map({sha, parents: [.parents[].sha], message: .commit.message, committer: .commit.committer.name, date: .commit.committer.date})' \
//	  > /tmp/backfill-commits.json
//	gh api "repos/OWNER/REPO/pulls?state=closed&base=BRANCH&per_page=100" \
//	  --jq '[.[] | select(.merged_at != null) | .merge_commit_sha]' > /tmp/backfill-prs.json
//	go run ./cmd/backfill-pushes -repo-id 943 -branch main \
//	  -commits /tmp/backfill-commits.json -prs /tmp/backfill-prs.json   # dry-run
//	go run ./cmd/backfill-pushes ... -apply                             # inserta + encola
//
// Dry-run por defecto: sin -apply no escribe nada. No re-aplicar tras un
// webhook que ya trajo el mismo push: el upsert dedup la fila, pero el
// PushReviewJob se encolaría de nuevo.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"

	"github.com/Andres39128/codeowl/backend/internal/backfill"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("backfill-pushes", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	repoID := flag.Int64("repo-id", 0, "id del repo en codeowl (obligatorio)")
	branch := flag.String("branch", "", "rama base del repo (obligatoria)")
	commitsPath := flag.String("commits", "", "JSON de commits de la rama base (obligatorio)")
	prsPath := flag.String("prs", "", "JSON de merge_commit_sha de PRs mergeados (obligatorio)")
	apply := flag.Bool("apply", false, "insertar y encolar (default: dry-run)")
	mergesOnly := flag.Bool("merges-only", true, "solo merges sin PR (S1); los commits lineales los captura el webhook en vivo")
	flag.Parse()

	if *repoID == 0 || *branch == "" || *commitsPath == "" || *prsPath == "" {
		flag.Usage()
		return fmt.Errorf("faltan flags obligatorios")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return fmt.Errorf("DATABASE_URL no está seteado (set -a; . ./.env; set +a)")
	}

	in, err := os.ReadFile(*commitsPath)
	if err != nil {
		return fmt.Errorf("leyendo %s: %w", *commitsPath, err)
	}
	prs, err := os.ReadFile(*prsPath)
	if err != nil {
		return fmt.Errorf("leyendo %s: %w", *prsPath, err)
	}
	var data []backfill.Commit
	if err := json.Unmarshal(in, &data); err != nil {
		return fmt.Errorf("parseando %s: %w", *commitsPath, err)
	}
	var mergedPRSHAs []string
	if err := json.Unmarshal(prs, &mergedPRSHAs); err != nil {
		return fmt.Errorf("parseando %s: %w", *prsPath, err)
	}
	mergedSet := make(map[string]bool, len(mergedPRSHAs))
	for _, sha := range mergedPRSHAs {
		mergedSet[sha] = true
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
	if !repo.Enabled {
		return fmt.Errorf("el repo %s/%s está deshabilitado", repo.Owner, repo.Name)
	}

	candidatos := backfill.Candidates(data, mergedSet)
	if *mergesOnly {
		filtrados := candidatos[:0]
		for _, c := range candidatos {
			if c.IsMerge() {
				filtrados = append(filtrados, c)
			}
		}
		candidatos = filtrados
	}
	// Los inserts van viejo→nuevo: el orden del listado de Trazabilidad
	// (created_at) refleja la historia real, no el orden inverso de la API.
	sort.Slice(candidatos, func(i, j int) bool {
		return candidatos[i].Date.Before(candidatos[j].Date)
	})

	slog.Info("backfill: candidatos detectados", "repo", repo.Owner+"/"+repo.Name,
		"branch", *branch, "commits", len(data), "candidatos", len(candidatos),
		"mode", map[bool]string{true: "apply", false: "dry-run"}[*apply])
	for _, c := range candidatos {
		fmt.Printf("%s %s %s %s\n", c.SHA[:7], c.Date.Format(time.DateOnly), c.Committer, primeraLinea(c.Message))
	}
	if !*apply {
		fmt.Println("dry-run: sin -apply no se escribe nada")
		return nil
	}

	q, err := jobs.New(ctx, st.Pool)
	if err != nil {
		return fmt.Errorf("abriendo la cola: %w", err)
	}
	insertados := 0
	for _, c := range candidatos {
		row, err := st.UpsertDirectPush(ctx, store.UpsertDirectPushParams{
			RepositoryID: repo.ID,
			Branch:       *branch,
			BeforeSha:    c.Before,
			AfterSha:     c.After,
			Committer:    c.Committer,
		})
		if err != nil {
			return fmt.Errorf("upsert del push %s: %w", c.After, err)
		}
		args, err := json.Marshal(jobs.PushReviewJobArgs{RepositoryID: repo.ID, PushID: row.ID})
		if err != nil {
			return fmt.Errorf("serializando args del push %s: %w", c.After, err)
		}
		if err := q.Enqueue(ctx, jobs.KindPushReview, args); err != nil {
			return fmt.Errorf("encolando el análisis del push %s: %w", c.After, err)
		}
		insertados++
	}
	slog.Info("backfill completo", "repo", repo.Owner+"/"+repo.Name, "pushes", insertados)
	return nil
}

// primeraLinea recorta el mensaje del commit para el listado del dry-run.
func primeraLinea(msg string) string {
	for i, r := range msg {
		if r == '\n' {
			return msg[:i]
		}
	}
	return msg
}
