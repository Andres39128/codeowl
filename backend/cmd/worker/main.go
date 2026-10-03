// Worker de codeowl: consume jobs River de Postgres (mapa: servicios.worker).
// F1: registra los workers del dominio — ReviewJob (pipeline de revisión),
// CleanupJob (retención diaria, §9.11), RotationJob (re-cifrado de master
// key, §9.2) y ReconcileJob (reconciliación de PRs, §3.5). F2 suma el
// ChatJob (respuestas a menciones @bot, §3.5/§6) y F4 el IndexJob (índice
// RAG de la rama default, §6 F4). SIGTERM drena ordenado: deja de tomar
// jobs y termina el en curso (§9.12; la ventana de gracia la fija
// TimeoutStopSec de la unidad).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/config"
	"github.com/Andres39128/codeowl/backend/internal/index"
	"github.com/Andres39128/codeowl/backend/internal/jobs"
	"github.com/Andres39128/codeowl/backend/internal/llm"
	"github.com/Andres39128/codeowl/backend/internal/review"
	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs/github"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil))) // logs JSON (§9.9)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("configuración inválida", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("no se pudo conectar a la base de datos", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.Ping(ctx); err != nil {
		slog.Error("la base de datos no responde", "err", err)
		os.Exit(1)
	}

	// Sin migraciones ni seed: la unidad api las ejecuta al arranque.
	q, err := jobs.New(ctx, st.Pool, jobs.Options{
		ReviewConcurrency: cfg.Stage2.ReviewConcurrency,
		ChatConcurrency:   cfg.Stage2.ChatConcurrency,
	})
	if err != nil {
		slog.Error("cola de jobs", "err", err)
		os.Exit(1)
	}

	// Deps del dominio: gateway LLM, adapter VCS, analyzer sandbox y topes
	// del pipeline (§4.2: el wiring llena review.Config desde Stage2Config).
	gateway := llm.New(st, cfg.MasterKey, llm.Limits{
		MaxPerReview:   cfg.Stage2.LLMMaxPerReview,
		MaxGlobal:      cfg.Stage2.LLMMaxGlobal,
		Timeout:        cfg.Stage2.LLMTimeout,
		MaxRetries:     cfg.Stage2.LLMMaxRetries,
		EmbedBatchSize: cfg.Stage2.EmbedBatchSize,
	})
	provider := github.New(st, cfg, q) // el webhook encola por la interfaz; el worker no publica por acá
	analyzer := analyze.NewRunner(analyze.Config{
		Image:     cfg.Stage2.AnalyzerImage,
		Memory:    cfg.Stage2.AnalyzerMemory,
		PidsLimit: cfg.Stage2.AnalyzerPidsLimit,
		Timeout:   cfg.Stage2.AnalyzerTimeout,
		TmpfsSize: cfg.Stage2.AnalyzerTmpfsSize,
	})
	reviewCfg := review.Config{
		DiffMaxLines:       cfg.Stage2.DiffMaxLines,
		DiffFileMaxLines:   cfg.Stage2.DiffFileMaxLines,
		AgentRetries:       cfg.Stage2.ReviewAgentRetries,
		DriftLines:         cfg.Stage2.ReviewDriftLines,
		DefaultProfile:     cfg.Stage2.ReviewDefaultProfile,
		CacheTTL:           cfg.Stage2.ReviewCacheTTL,
		CacheMaxEntries:    cfg.Stage2.ReviewCacheMaxEntries,
		Concurrency:        cfg.Stage2.LLMMaxPerReview,
		RiskSensitivePaths: cfg.Stage2.RiskSensitivePaths,
	}

	if err := q.Register(
		&jobs.ReviewJobWorker{
			Store: st, Gateway: gateway, Analyzer: analyzer, Provider: provider, Queue: q, Config: reviewCfg,
			Retriever: jobs.NewIndexRetriever(st, gateway, cfg.Stage2.RetrievalTopK),
		},
		&jobs.ChatJobWorker{
			Store: st, Gateway: gateway, Provider: provider, Queue: q,
			Config: review.ChatConfig{
				DiffMaxLines:    cfg.Stage2.ChatDiffMaxLines,
				TestMaxSnippets: cfg.Stage2.ChatTestMaxSnippets,
			},
		},
		&jobs.CleanupJobWorker{
			Store: st, RetentionDays: cfg.Stage2.CleanupRetentionDays,
		},
		&jobs.RotationJobWorker{
			Store: st, NewKey: cfg.MasterKey, OldKey: cfg.Stage2.MasterKeyPrevious,
		},
		&jobs.ReconcileJobWorker{
			Store: st, Provider: provider,
		},
		&jobs.IndexJobWorker{
			Store: st, Gateway: gateway, Extractor: analyzer, Provider: provider,
			Config: index.Config{
				MaxSymbolsPerRun: cfg.Stage2.IndexMaxSymbolsPerRun,
				DefaultProfile:   cfg.Stage2.ReviewDefaultProfile,
			},
		},
	); err != nil {
		slog.Error("registrando workers", "err", err)
		os.Exit(1)
	}
	q.RegisterPeriodic(jobs.CleanupPeriodic())

	defer func() { _ = q.Stop(ctx) }()
	if err := q.Start(ctx); err != nil {
		slog.Error("arrancando la cola", "err", err)
		os.Exit(1)
	}
	slog.Info("worker corriendo",
		"review_concurrency", cfg.Stage2.ReviewConcurrency,
		"chat_concurrency", cfg.Stage2.ChatConcurrency,
		"cleanup_retention_days", cfg.Stage2.CleanupRetentionDays)
	<-ctx.Done()
	slog.Info("apagando el worker: drenando el job en curso")
}
