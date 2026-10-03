// Package config carga y valida la configuración por etapas (mapa: backend.config).
//
// Etapa 1 (arranque): esenciales — DB, master key, escucha, sesión y seed del
// admin. Load() falla si falta o es inválido alguno, listando todos los
// problemas en un solo error.
//
// Etapa 2: credenciales de la GitHub App y topes de pipeline. Se modelan y se
// cargan si están presentes (un valor presente con formato inválido sí falla
// el arranque); su ausencia es válida — las credenciales de GitHub validan al
// conectar el primer repo (F1, Stage2Config.ValidateGitHub) y F0 corre sin ellas.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults de .env.example (guía §3.3/§3.4: materializados desde F0).
const (
	DefaultAPIAddr               = ":8080"
	DefaultSessionTTL            = 24 * time.Hour
	DefaultPasswordMinLength     = 12
	DefaultLoginMaxFails         = 5       // §3.4: backoff de login tras N fallos por usuario
	DefaultWorkdirDiskBudget     = 2 << 30 // 2 GiB
	DefaultDiffMaxLines          = 4000
	DefaultDiffFileMaxLines      = 1000              // §9.6: tope análogo por archivo (solo SAST)
	DefaultReviewAgentRetries    = 2                 // §9.8: reintentos por salida malformada del agente
	DefaultReviewDriftLines      = 3                 // §3.6.3: tolerancia de drift del ancla de dedup
	DefaultReviewProfile         = "assertive"       // §9.5: perfil global para repos sin review.yaml
	DefaultReviewCacheTTL        = time.Hour         // §9.6: TTL de la cache de resultados
	DefaultReviewCacheMaxEntries = 100               // §9.6: tope de entradas de la cache
	DefaultLLMMaxPerReview       = 4                 // §9.6: llamadas LLM simultáneas por review
	DefaultLLMMaxGlobal          = 8                 // §9.6: llamadas LLM simultáneas del gateway
	DefaultLLMTimeout            = 120 * time.Second // §9.7: timeout por llamada LLM
	DefaultLLMMaxRetries         = 3                 // §9.7: reintentos por proveedor ante 429/5xx
	DefaultEmbedBatchSize        = 64                // §9.6: textos máximos por llamada /v1/embeddings
	DefaultIndexMaxSymbolsPerRun = 2000              // §9.6: tope de símbolos embebidos por IndexJob (resume §9.6)
	DefaultWebhookMaxBytes       = 25 << 20          // §9.3: tope propio = el de GitHub (25 MB)
	DefaultVCSTimeout            = 30 * time.Second  // timeout por llamada HTTP al VCS
	// GitLab (§9.3/§3.5/§3.6.1.1): frescura anti-replay, username del bot
	// (anti-bucle) y TTL de la cache del tip de la rama base.
	DefaultWebhookTimestampTolerance = 5 * time.Minute // default del SDK Standard Webhooks
	DefaultGitLabBotUsername         = "codeowl"
	DefaultBranchTipCacheTTL         = 30 * time.Second
	// Chat (§3.5/§9.6): username del bot en GitHub para las menciones del
	// chat, tope de diff que entra al prompt del chat (presupuesto propio) y
	// tope de pruebas que genera /tests.
	DefaultGitHubBotUsername    = "codeowl-bot"
	DefaultChatDiffMaxLines     = 5000
	DefaultChatTestMaxSnippets  = 3
	DefaultAnalyzerImage        = "localhost/codeowl-analyzer:latest"
	DefaultAnalyzerMemory       = "1g"              // §9.4: tope de RAM del sandbox
	DefaultAnalyzerPidsLimit    = 256               // §9.4: tope de procesos del sandbox
	DefaultAnalyzerTimeout      = 120 * time.Second // timeout duro del sandbox (§9.4)
	DefaultAnalyzerTmpfsSize    = "512m"            // §9.4: tmpfs de /tmp para caches de linters
	DefaultReviewConcurrency    = 2                 // §9.6: ReviewJobs concurrentes en el worker
	DefaultChatConcurrency      = 2                 // §9.6: slots propios del ChatJob (F2)
	DefaultCleanupRetentionDays = 30                // §9.11: retención de webhook_deliveries
)

// masterKeySize es el tamaño en bytes de la clave AES-256 (guía §9.2).
const masterKeySize = 32

// reviewProfiles es el conjunto cerrado de perfiles de revisión (§9.5).
// Espejo del conjunto de repoconfig (config no importa paquetes internos).
var reviewProfiles = map[string]bool{"chill": true, "assertive": true, "strict": true}

type Config struct {
	APIAddr           string
	DatabaseURL       string
	MasterKey         []byte
	SessionTTL        time.Duration
	PasswordMinLength int
	LoginMaxFails     int // fallos consecutivos antes del backoff (§3.4)
	AdminUsername     string
	AdminPassword     string
	Stage2            Stage2Config
}

// Stage2Config agrupa lo que no es esencial al arranque: credenciales de la
// GitHub App (se validan al conectar el primer repo — F1) y topes de pipeline.
type Stage2Config struct {
	GitHubAppID         string
	GitHubAppPrivateKey string
	GitHubWebhookSecret string
	WorkdirDiskBudget   int64 // bytes por workdir de job (§3.3)
	DiffMaxLines        int   // tope de diff solo-resumen (§9.6)
	DiffFileMaxLines    int   // tope de diff por archivo: solo SAST (§9.6)
	// Topes del pipeline de review (§3.6/§9.6/§9.8): los consumen internal/review.
	ReviewAgentRetries    int           // reintentos por salida malformada del agente
	ReviewDriftLines      int           // tolerancia de drift del ancla de dedup (±N líneas)
	ReviewDefaultProfile  string        // perfil global para repos sin review.yaml (§9.5)
	ReviewCacheTTL        time.Duration // TTL de la cache de resultados
	ReviewCacheMaxEntries int           // tope de entradas de la cache
	MasterKeyPrevious     []byte        // rotación de master key (§9.2), opcional
	// Topes del gateway LLM (§9.6/§9.7): los consumen internal/llm.
	LLMMaxPerReview int
	LLMMaxGlobal    int
	LLMTimeout      time.Duration
	LLMMaxRetries   int
	EmbedBatchSize  int // textos máximos por llamada /v1/embeddings (F4, §6)
	// Indexación simbólica RAG (F4, §6/§9.6): la consume internal/index (T6
	// threadea ReviewDefaultProfile como DefaultProfile del índice).
	IndexMaxSymbolsPerRun int // tope de símbolos embebidos por IndexJob; agotado → parcial con resume
	// Webhook + VCS (§9.3/§4.5): los consumen internal/vcs.
	WebhookMaxBytes int64
	VCSTimeout      time.Duration
	// GitLab (§9.3/§3.5/§3.6.1.1): los consume internal/vcs/gitlab.
	WebhookTimestampTolerance time.Duration // frescura del timestamp Standard Webhooks (replay)
	GitLabBotUsername         string        // anti-bucle del chat (GitLab no expone flag de bot)
	BranchTipCacheTTL         time.Duration // cache del tip de la rama base (§3.6.1.1)
	// Chat (§3.5/§9.6): los consumen internal/vcs/github e internal/review.
	GitHubBotUsername   string // mención del bot en comentarios del chat (F2)
	ChatDiffMaxLines    int    // tope de diff que entra al prompt del chat (§9.6)
	ChatTestMaxSnippets int    // tope de pruebas generadas por /tests (§9.6)
	// Sandbox analyzer (§9.4): los consumen internal/analyze.
	AnalyzerImage     string
	AnalyzerMemory    string
	AnalyzerPidsLimit int
	AnalyzerTimeout   time.Duration
	AnalyzerTmpfsSize string
	// Jobs del worker (§9.6/§9.11): los consume internal/jobs.
	ReviewConcurrency    int // ReviewJobs concurrentes (cola review)
	ChatConcurrency      int // slots propios del ChatJob (cola chat, F2)
	CleanupRetentionDays int // retención de webhook_deliveries (CleanupJob)
}

// ValidateGitHub verifica que las credenciales de la App estén completas.
// Se llama al conectar el primer repo GitHub (guía §6 F1), no al arranque.
func (s Stage2Config) ValidateGitHub() error {
	var faltan []string
	if s.GitHubAppID == "" {
		faltan = append(faltan, "GITHUB_APP_ID")
	}
	if s.GitHubAppPrivateKey == "" {
		faltan = append(faltan, "GITHUB_APP_PRIVATE_KEY")
	}
	if s.GitHubWebhookSecret == "" {
		faltan = append(faltan, "GITHUB_WEBHOOK_SECRET")
	}
	if len(faltan) > 0 {
		return fmt.Errorf("credenciales de GitHub App incompletas, faltan: %s", strings.Join(faltan, ", "))
	}
	return nil
}

// Load carga la configuración del entorno. Falla rápido listando todos los
// errores encontrados, no solo el primero.
func Load() (*Config, error) {
	var errs []string

	cfg := &Config{}
	cfg.APIAddr = envOr("API_ADDR", DefaultAPIAddr)

	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	if cfg.DatabaseURL == "" {
		errs = append(errs, "DATABASE_URL es obligatorio")
	}

	key, err := loadKey("MASTER_KEY", true)
	if err != nil {
		errs = append(errs, err.Error())
	}
	cfg.MasterKey = key

	ttl, err := loadDuration("SESSION_TTL", DefaultSessionTTL)
	if err != nil {
		errs = append(errs, err.Error())
	}
	cfg.SessionTTL = ttl

	minLen, err := loadInt("PASSWORD_MIN_LENGTH", DefaultPasswordMinLength)
	if err != nil {
		errs = append(errs, err.Error())
	} else if minLen < 1 {
		errs = append(errs, "PASSWORD_MIN_LENGTH debe ser mayor o igual a 1")
	}
	cfg.PasswordMinLength = minLen

	maxFails, err := loadInt("LOGIN_MAX_FAILS", DefaultLoginMaxFails)
	if err != nil {
		errs = append(errs, err.Error())
	} else if maxFails < 1 {
		errs = append(errs, "LOGIN_MAX_FAILS debe ser mayor o igual a 1")
	}
	cfg.LoginMaxFails = maxFails

	user, err := secret("ADMIN_USERNAME")
	if err != nil {
		errs = append(errs, err.Error())
	} else if user == "" {
		errs = append(errs, "ADMIN_USERNAME es obligatorio (seed del admin, guía §3.4)")
	}
	cfg.AdminUsername = user

	pass, err := secret("ADMIN_PASSWORD")
	if err != nil {
		errs = append(errs, err.Error())
	} else if pass == "" {
		errs = append(errs, "ADMIN_PASSWORD es obligatorio (seed del admin, guía §3.4)")
	}
	cfg.AdminPassword = pass

	// Etapa 2: presente con formato inválido falla el arranque; ausente es válido.
	s2 := Stage2Config{}
	s2.GitHubAppID = os.Getenv("GITHUB_APP_ID")

	priv, err := secret("GITHUB_APP_PRIVATE_KEY")
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.GitHubAppPrivateKey = priv

	whSecret, err := secret("GITHUB_WEBHOOK_SECRET")
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.GitHubWebhookSecret = whSecret

	prev, err := loadKey("MASTER_KEY_PREVIOUS", false)
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.MasterKeyPrevious = prev

	budget, err := loadDiskSize("WORKDIR_DISK_BUDGET", DefaultWorkdirDiskBudget)
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.WorkdirDiskBudget = budget

	diffMax, err := loadInt("DIFF_MAX_LINES", DefaultDiffMaxLines)
	if err != nil {
		errs = append(errs, err.Error())
	} else if diffMax < 1 {
		errs = append(errs, "DIFF_MAX_LINES debe ser mayor o igual a 1")
	}
	s2.DiffMaxLines = diffMax

	fileMax, err := loadInt("DIFF_FILE_MAX_LINES", DefaultDiffFileMaxLines)
	if err != nil {
		errs = append(errs, err.Error())
	} else if fileMax < 1 {
		errs = append(errs, "DIFF_FILE_MAX_LINES debe ser mayor o igual a 1")
	}
	s2.DiffFileMaxLines = fileMax

	agentRetries, err := loadInt("REVIEW_AGENT_RETRIES", DefaultReviewAgentRetries)
	if err != nil {
		errs = append(errs, err.Error())
	} else if agentRetries < 0 {
		errs = append(errs, "REVIEW_AGENT_RETRIES debe ser mayor o igual a 0")
	}
	s2.ReviewAgentRetries = agentRetries

	drift, err := loadInt("REVIEW_DRIFT_LINES", DefaultReviewDriftLines)
	if err != nil {
		errs = append(errs, err.Error())
	} else if drift < 0 {
		errs = append(errs, "REVIEW_DRIFT_LINES debe ser mayor o igual a 0")
	}
	s2.ReviewDriftLines = drift

	profile := strings.TrimSpace(os.Getenv("REVIEW_DEFAULT_PROFILE"))
	switch {
	case profile == "":
		profile = DefaultReviewProfile
	case !reviewProfiles[profile]:
		errs = append(errs, "REVIEW_DEFAULT_PROFILE debe ser chill, assertive o strict (§9.5)")
	}
	s2.ReviewDefaultProfile = profile

	cacheTTL, err := loadDuration("REVIEW_CACHE_TTL", DefaultReviewCacheTTL)
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.ReviewCacheTTL = cacheTTL

	cacheMax, err := loadInt("REVIEW_CACHE_MAX_ENTRIES", DefaultReviewCacheMaxEntries)
	if err != nil {
		errs = append(errs, err.Error())
	} else if cacheMax < 1 {
		errs = append(errs, "REVIEW_CACHE_MAX_ENTRIES debe ser mayor o igual a 1")
	}
	s2.ReviewCacheMaxEntries = cacheMax

	maxPerReview, err := loadInt("LLM_MAX_PER_REVIEW", DefaultLLMMaxPerReview)
	if err != nil {
		errs = append(errs, err.Error())
	} else if maxPerReview < 1 {
		errs = append(errs, "LLM_MAX_PER_REVIEW debe ser mayor o igual a 1")
	}
	s2.LLMMaxPerReview = maxPerReview

	maxGlobal, err := loadInt("LLM_MAX_GLOBAL", DefaultLLMMaxGlobal)
	if err != nil {
		errs = append(errs, err.Error())
	} else if maxGlobal < 1 {
		errs = append(errs, "LLM_MAX_GLOBAL debe ser mayor o igual a 1")
	}
	s2.LLMMaxGlobal = maxGlobal

	llmTimeout, err := loadDuration("LLM_TIMEOUT", DefaultLLMTimeout)
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.LLMTimeout = llmTimeout

	maxRetries, err := loadInt("LLM_MAX_RETRIES", DefaultLLMMaxRetries)
	if err != nil {
		errs = append(errs, err.Error())
	} else if maxRetries < 0 {
		errs = append(errs, "LLM_MAX_RETRIES debe ser mayor o igual a 0")
	}
	s2.LLMMaxRetries = maxRetries

	embedBatch, err := loadInt("EMBED_BATCH_SIZE", DefaultEmbedBatchSize)
	if err != nil {
		errs = append(errs, err.Error())
	} else if embedBatch < 1 {
		errs = append(errs, "EMBED_BATCH_SIZE debe ser mayor o igual a 1")
	}
	s2.EmbedBatchSize = embedBatch

	idxMax, err := loadInt("INDEX_MAX_SYMBOLS_PER_RUN", DefaultIndexMaxSymbolsPerRun)
	if err != nil {
		errs = append(errs, err.Error())
	} else if idxMax < 1 {
		errs = append(errs, "INDEX_MAX_SYMBOLS_PER_RUN debe ser mayor o igual a 1")
	}
	s2.IndexMaxSymbolsPerRun = idxMax

	whBytes, err := loadDiskSize("WEBHOOK_MAX_BYTES", DefaultWebhookMaxBytes)
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.WebhookMaxBytes = whBytes

	vcsTimeout, err := loadDuration("VCS_TIMEOUT", DefaultVCSTimeout)
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.VCSTimeout = vcsTimeout

	tolerance, err := loadDuration("WEBHOOK_TIMESTAMP_TOLERANCE", DefaultWebhookTimestampTolerance)
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.WebhookTimestampTolerance = tolerance

	s2.GitLabBotUsername = envOr("GITLAB_BOT_USERNAME", DefaultGitLabBotUsername)

	s2.GitHubBotUsername = envOr("GITHUB_BOT_USERNAME", DefaultGitHubBotUsername)

	chatDiffMax, err := loadInt("CHAT_DIFF_MAX_LINES", DefaultChatDiffMaxLines)
	if err != nil {
		errs = append(errs, err.Error())
	} else if chatDiffMax < 1 {
		errs = append(errs, "CHAT_DIFF_MAX_LINES debe ser mayor o igual a 1")
	}
	s2.ChatDiffMaxLines = chatDiffMax

	testMax, err := loadInt("CHAT_TEST_MAX_SNIPPETS", DefaultChatTestMaxSnippets)
	if err != nil {
		errs = append(errs, err.Error())
	} else if testMax < 1 {
		errs = append(errs, "CHAT_TEST_MAX_SNIPPETS debe ser mayor o igual a 1")
	}
	s2.ChatTestMaxSnippets = testMax

	branchTTL, err := loadDuration("BRANCH_TIP_CACHE_TTL", DefaultBranchTipCacheTTL)
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.BranchTipCacheTTL = branchTTL

	s2.AnalyzerImage = envOr("ANALYZER_IMAGE", DefaultAnalyzerImage)

	memory := strings.TrimSpace(os.Getenv("ANALYZER_MEMORY"))
	if memory == "" {
		memory = DefaultAnalyzerMemory
	}
	s2.AnalyzerMemory = memory

	pids, err := loadInt("ANALYZER_PIDS_LIMIT", DefaultAnalyzerPidsLimit)
	if err != nil {
		errs = append(errs, err.Error())
	} else if pids < 1 {
		errs = append(errs, "ANALYZER_PIDS_LIMIT debe ser mayor o igual a 1")
	}
	s2.AnalyzerPidsLimit = pids

	analyzerTimeout, err := loadDuration("ANALYZER_TIMEOUT", DefaultAnalyzerTimeout)
	if err != nil {
		errs = append(errs, err.Error())
	}
	s2.AnalyzerTimeout = analyzerTimeout

	tmpfs := strings.TrimSpace(os.Getenv("ANALYZER_TMPFS_SIZE"))
	if tmpfs == "" {
		tmpfs = DefaultAnalyzerTmpfsSize
	}
	s2.AnalyzerTmpfsSize = tmpfs

	reviewConc, err := loadInt("REVIEW_CONCURRENCY", DefaultReviewConcurrency)
	if err != nil {
		errs = append(errs, err.Error())
	} else if reviewConc < 1 {
		errs = append(errs, "REVIEW_CONCURRENCY debe ser mayor o igual a 1")
	}
	s2.ReviewConcurrency = reviewConc

	chatConc, err := loadInt("CHAT_CONCURRENCY", DefaultChatConcurrency)
	if err != nil {
		errs = append(errs, err.Error())
	} else if chatConc < 1 {
		errs = append(errs, "CHAT_CONCURRENCY debe ser mayor o igual a 1")
	}
	s2.ChatConcurrency = chatConc

	retentionDays, err := loadInt("CLEANUP_RETENTION_DAYS", DefaultCleanupRetentionDays)
	if err != nil {
		errs = append(errs, err.Error())
	} else if retentionDays < 1 {
		errs = append(errs, "CLEANUP_RETENTION_DAYS debe ser mayor o igual a 1")
	}
	s2.CleanupRetentionDays = retentionDays

	cfg.Stage2 = s2

	if len(errs) > 0 {
		return nil, fmt.Errorf("configuración inválida:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return cfg, nil
}

// secret devuelve el valor directo de la variable o, si está seteada, el
// contenido de su variante _FILE (§9.2; la variante gana — en Quadlet siempre
// se usa _FILE). Un _FILE vacío equivale a no seteado (convención de
// .env.example). El contenido se recorta.
func secret(name string) (string, error) {
	if file := os.Getenv(name + "_FILE"); file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: no se pudo leer %q: %w", name, file, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return os.Getenv(name), nil
}

// loadKey carga una master key: vacía si es opcional y no está seteada;
// base64 de masterKeySize bytes si está presente (guía §9.2).
func loadKey(name string, required bool) ([]byte, error) {
	raw, err := secret(name)
	if err != nil {
		return nil, err
	}
	if raw == "" {
		if required {
			return nil, fmt.Errorf("%s es obligatoria (base64 de %d bytes — generala con: openssl rand -base64 %d)", name, masterKeySize, masterKeySize)
		}
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s debe ser base64 válido (generala con: openssl rand -base64 %d)", name, masterKeySize)
	}
	if len(key) != masterKeySize {
		return nil, fmt.Errorf("%s debe decodificar a exactamente %d bytes (tiene %d)", name, masterKeySize, len(key))
	}
	return key, nil
}

func loadDuration(name string, def time.Duration) (time.Duration, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s debe ser una duración válida (ej. 24h, 30m): %v", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s debe ser mayor a cero", name)
	}
	return d, nil
}

func loadInt(name string, def int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s debe ser un entero: %v", name, err)
	}
	return n, nil
}

// loadDiskSize interpreta tamaños tipo "2GiB", "512MiB", "1.5GiB" o bytes
// planos (guía §3.3: presupuesto de disco por workdir).
func loadDiskSize(name string, def int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	units := []struct {
		suf  string
		mult float64
	}{
		{"KIB", 1 << 10}, {"MIB", 1 << 20}, {"GIB", 1 << 30}, {"TIB", 1 << 40},
	}
	up := strings.ToUpper(raw)
	for _, u := range units {
		if strings.HasSuffix(up, u.suf) {
			n, err := strconv.ParseFloat(strings.TrimSpace(raw[:len(raw)-len(u.suf)]), 64)
			if err != nil {
				return 0, fmt.Errorf("%s debe ser un tamaño como 2GiB, 512MiB o bytes planos", name)
			}
			return int64(n * u.mult), nil
		}
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s debe ser un tamaño como 2GiB, 512MiB o bytes planos", name)
	}
	return n, nil
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
