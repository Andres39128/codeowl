// Package index construye el índice simbólico RAG del repo (F4, guía §6):
// chunking simbólico del analyzer (--symbols), embeddings del rol embedding
// vía el gateway y persistencia en repo_index. Resume por file_hash (§9.6:
// hash igual y mismo embedding_model → se salta), presupuesto propio por
// corrida (§9.6: agotado → queda PARCIAL y el próximo IndexJob lo retoma) y
// wipe completo ante cambio de modelo de embeddings (§9.6). Retrieve (T5)
// consume el índice y embebe las queries con SymbolQueryText.
package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/repoconfig"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// roleEmbedding es el rol del gateway que provee embeddings (§3.3).
const roleEmbedding = "embedding"

// defaultReviewProfile espejo de config.DefaultReviewProfile (§4.2: el
// wiring del worker llena Config desde Stage2Config; este cubre el
// zero-value y los tests).
const defaultReviewProfile = "assertive"

// defaultMaxSymbolsPerRun espejo de config.DefaultIndexMaxSymbolsPerRun.
const defaultMaxSymbolsPerRun = 2000

var (
	// ErrNoEmbeddingProvider: guard idempotente — T6 lo chequea antes de
	// encolar el IndexJob y el core lo vuelve a chequear al correr.
	ErrNoEmbeddingProvider = errors.New("sin proveedor de embeddings enabled (rol embedding)")

	// ErrDimsMismatch: backstop de runtime — el guard de settings (T9) es la
	// primera línea de defensa; si el proveedor sirve dims distintas al
	// vector(1536) del catálogo, el INSERT de pgvector fallaría: se detecta
	// ANTES (largo del vector vs typmod del catálogo) y se tipa.
	ErrDimsMismatch = errors.New("dims del embedding no coinciden con el vector del catálogo de repo_index")
)

// SymbolQueryText es el formato compartido del texto embebido (contrato
// §6 F4): Index lo usa por símbolo y Retrieve (T5) DEBE embeber las queries
// con exactamente este formato — la similitud coseno exige el mismo espacio.
// Formato estable y determinista: "file\nkind symbol" — barato, sin
// re-leer código fuente.
func SymbolQueryText(file, kind, symbol string) string {
	return file + "\n" + kind + " " + symbol
}

// Store es el subconjunto de la store que consume Index (§4.5: interfaces
// donde se consumen). *store.Store la satisface.
type Store interface {
	ListEnabledLlmProvidersByRole(ctx context.Context, role string) ([]store.LlmProvider, error)
	GetRepoIndexDims(ctx context.Context) (int32, error)
	GetRepoIndexEmbeddingModel(ctx context.Context, repositoryID int64) (string, error)
	ListRepoIndexFiles(ctx context.Context, repositoryID int64) ([]store.ListRepoIndexFilesRow, error)
	DeleteRepoIndexByRepo(ctx context.Context, repositoryID int64) error
	DeleteRepoIndexByRepoAndFiles(ctx context.Context, arg store.DeleteRepoIndexByRepoAndFilesParams) error
	CreateRepoIndexRow(ctx context.Context, arg store.CreateRepoIndexRowParams) (int64, error)
}

// Gateway es lo único que Index le pide al LLM (mapa: el índice consume el
// rol embedding). *llm.Gateway la satisface.
type Gateway interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Extractor es el cliente del sandbox en modo --symbols (§6 F4).
// *analyze.Runner la satisface.
type Extractor interface {
	ExtractSymbols(ctx context.Context, workdir string) ([]analyze.Symbol, error)
}

// Config son los topes de la corrida de indexación — config (§9.6). El
// wiring del worker (T6) los llena desde config.Stage2Config.
type Config struct {
	MaxSymbolsPerRun int    // tope de símbolos embebidos por corrida (§9.6: default 2000)
	DefaultProfile   string // perfil global para repos sin review.yaml (§9.5)
}

// DefaultConfig devuelve la config por defecto (espejo de config.Stage2Config
// — §4.2).
func DefaultConfig() Config {
	return Config{MaxSymbolsPerRun: defaultMaxSymbolsPerRun, DefaultProfile: defaultReviewProfile}
}

// normalized clampea los valores fuera de rango a los defaults: la config
// inválida nunca cuelga la indexación (mismo criterio que review.Config).
func (c Config) normalized() Config {
	d := DefaultConfig()
	if c.MaxSymbolsPerRun < 1 {
		c.MaxSymbolsPerRun = d.MaxSymbolsPerRun
	}
	if !repoconfig.IsValidProfile(c.DefaultProfile) {
		c.DefaultProfile = defaultReviewProfile
	}
	return c
}

// Result es el desenlace de la corrida para el caller (job, T6): recuentos
// honestos (§9.6 — jamás recorte silencioso).
type Result struct {
	FilesIndexed   int  // archivos (re)indexados en esta corrida
	SymbolsIndexed int  // filas creadas en repo_index
	FilesSkipped   int  // archivos que pasaron filtros y NO se tocaron: resume-skip (hash+modelo iguales, §9.6) o ilegibles/fuera del clon
	Truncated      bool // presupuesto agotado con archivos pendientes: quedan para el próximo IndexJob (resume §9.6)
}

// Index ejecuta una corrida de indexación del repo (§6 F4): symbols →
// filtros → hash → resume → embed → upsert. El workdir es el clon shallow
// de la rama default EN SU ESTADO AL EJECUTAR: HEAD es su tip por
// definición, no hay merge-base de PR ni superficie de inyección de
// review.yaml (§9.5 n/a acá — el operador controla la rama default).
//
// Errores devueltos son de infraestructura reintentable (diff, BD, gateway
// tras agotar failover): el job (T6) reintenta y los upserts parciales ya
// hechos persisten — el resume por hash hace el reintento barato (§9.6).
func Index(ctx context.Context, st Store, gw Gateway, ex Extractor, repo store.Repository, workdir string, cfg Config) (*Result, error) {
	cfg = cfg.normalized()
	res := &Result{}

	// (1) Proveedor de embeddings vigente: el primero enabled del rol
	// (ORDER BY priority, id — cadena de failover §3.3). HONESTIDAD de la
	// anotación: Embed conmuta de proveedor ante fallo (§9.7) sin reportar
	// cuál sirvió; la anotación embedding_model registra el de MÁXIMA
	// prioridad — es la procedencia declarada del índice, aproximada si el
	// failover entra a mitad de corrida (mismas dims garantizadas por el
	// gateway, o error).
	providers, err := st.ListEnabledLlmProvidersByRole(ctx, roleEmbedding)
	if err != nil {
		return nil, fmt.Errorf("listando proveedores del rol %s: %w", roleEmbedding, err)
	}
	if len(providers) == 0 {
		return nil, ErrNoEmbeddingProvider
	}
	model := providers[0].Model

	// (2) Dims del catálogo: el typmod de repo_index.embedding ES la
	// cantidad de dims (§3.3: la config jamás diverge del DDL).
	dims, err := st.GetRepoIndexDims(ctx)
	if err != nil {
		return nil, fmt.Errorf("leyendo dims del catálogo: %w", err)
	}
	if dims < 1 {
		return nil, fmt.Errorf("%w: el catálogo reporta dims=%d", ErrDimsMismatch, dims)
	}

	// (3) Config efectiva (§9.5): review.yaml leído del tip del clon (HEAD).
	head, err := exec.CommandContext(ctx, "git", "-C", workdir, "rev-parse", "HEAD").Output()
	if err != nil {
		return nil, fmt.Errorf("resolviendo HEAD del clon %s: %w", workdir, err)
	}
	rc := repoconfig.Load(ctx, workdir, strings.TrimSpace(string(head)), repo, cfg.DefaultProfile)

	// (4) Símbolos del sandbox, filtrados y agrupados por archivo.
	syms, err := ex.ExtractSymbols(ctx, workdir)
	if err != nil {
		return nil, fmt.Errorf("extrayendo símbolos: %w", err)
	}
	byFile, order := groupSymbols(syms, rc.PathFilters)

	// (5) Hash de contenido por archivo: la huella del resume-skip (§9.6).
	// Un archivo ilegible (borrado entre el sandbox y acá) o con ruta fuera
	// del clon se salta con registro — jamás frena la corrida (§6 F4).
	hashes := make(map[string]string, len(order))
	kept := make([]string, 0, len(order))
	for _, f := range order {
		h, err := fileHash(workdir, f)
		if err != nil {
			slog.Warn("index: archivo omitido, no se pudo hashear", "file", f, "error", err)
			res.FilesSkipped++
			continue
		}
		hashes[f] = h
		kept = append(kept, f)
	}

	// (6) Cambio de modelo de embeddings (§9.6): índice con otro modelo →
	// wipe completo ANTES de insertar (los hashes previos dejan de valer).
	// Índice vacío → pgx.ErrNoRows: primera corrida, nada que borrar.
	curModel, err := st.GetRepoIndexEmbeddingModel(ctx, repo.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		curModel = ""
	case err != nil:
		return nil, fmt.Errorf("leyendo el modelo vigente del índice: %w", err)
	case curModel != model:
		slog.Info("index: cambió el modelo de embeddings, wipe completo del índice (§9.6)",
			"repo", repo.ID, "antes", curModel, "ahora", model)
		if err := st.DeleteRepoIndexByRepo(ctx, repo.ID); err != nil {
			return nil, fmt.Errorf("borrando el índice por cambio de modelo: %w", err)
		}
		curModel = ""
	}

	// Resume-skip (§9.6): hash igual y MISMO embedding_model → el archivo ya
	// está al día. Tras un wipe el mapa queda vacío: todo se re-indexa.
	resume := make(map[string]bool, len(kept))
	if curModel != "" {
		files, err := st.ListRepoIndexFiles(ctx, repo.ID)
		if err != nil {
			return nil, fmt.Errorf("listando archivos indexados: %w", err)
		}
		for _, f := range files {
			if f.EmbeddingModel == model && hashes[f.File] == f.FileHash {
				resume[f.File] = true
			}
		}
	}

	// (7) Presupuesto de símbolos por corrida (§9.6): archivos en orden
	// determinista (lexicográfico); el corte es ENTRE archivos — un archivo
	// que no entra entero no se toca (insertar la mitad de sus símbolos los
	// congelaría: el resume-skip del hash igual no los re-visitaría).
	budget := cfg.MaxSymbolsPerRun
	for _, f := range kept {
		if resume[f] {
			res.FilesSkipped++
			continue
		}
		fileSyms := byFile[f]
		if len(fileSyms) > budget {
			res.Truncated = true
			break
		}
		budget -= len(fileSyms)
		if err := indexFile(ctx, st, gw, repo.ID, f, fileSyms, hashes[f], model, dims); err != nil {
			return nil, err
		}
		res.FilesIndexed++
		res.SymbolsIndexed += len(fileSyms)
	}
	return res, nil
}

// groupSymbols filtra (path_filters §9.5, kind del conjunto cerrado §3.3),
// deduplica por (file, kind, symbol) conservando el primer orden de
// documento y agrupa por archivo. El orden de archivos es lexicográfico:
// determinismo para el presupuesto y los recuentos (§9.6).
func groupSymbols(syms []analyze.Symbol, filters []string) (map[string][]analyze.Symbol, []string) {
	byFile := map[string][]analyze.Symbol{}
	seen := map[string]bool{}
	for _, s := range syms {
		if !analyze.IsValidKind(s.Kind) || !repoconfig.MatchPath(filters, s.File) {
			continue
		}
		key := s.File + "\x00" + s.Kind + "\x00" + s.Symbol
		if seen[key] {
			continue
		}
		seen[key] = true
		byFile[s.File] = append(byFile[s.File], s)
	}
	order := make([]string, 0, len(byFile))
	for f := range byFile {
		order = append(order, f)
	}
	sort.Strings(order)
	return byFile, order
}

// indexFile re-indexa UN archivo: embebe sus símbolos, borra sus filas
// viejas (§9.6) y crea una por símbolo con la anotación de modelo y dims del
// catálogo. Las filas conservan el orden de documento del extractor.
func indexFile(ctx context.Context, st Store, gw Gateway, repoID int64, file string, syms []analyze.Symbol, hash, model string, dims int32) error {
	texts := make([]string, len(syms))
	for i, s := range syms {
		texts[i] = SymbolQueryText(file, s.Kind, s.Symbol)
	}
	vecs, err := gw.Embed(ctx, texts)
	if err != nil {
		return fmt.Errorf("embebiendo %s (%d símbolos): %w", file, len(syms), err)
	}
	if len(vecs) != len(texts) {
		return fmt.Errorf("el gateway devolvió %d vectores para %d textos de %s", len(vecs), len(texts), file)
	}
	// Backstop de dims (§3.3): contrastar el largo del vector contra el
	// typmod del catálogo ANTES del INSERT convierte el rechazo de pgvector
	// en ErrDimsMismatch. El gateway garantiza dims uniformes por corrida,
	// así que contrastar el primero del lote cubre el archivo entero — no
	// hay caso en que el INSERT mismo falle por dims que este chequeo no vea.
	if len(vecs[0]) != int(dims) {
		return fmt.Errorf("%w: el proveedor sirve %d dims, el catálogo exige %d", ErrDimsMismatch, len(vecs[0]), dims)
	}
	if err := st.DeleteRepoIndexByRepoAndFiles(ctx, store.DeleteRepoIndexByRepoAndFilesParams{
		RepositoryID: repoID,
		Files:        []string{file},
	}); err != nil {
		return fmt.Errorf("borrando filas viejas de %s: %w", file, err)
	}
	for i, s := range syms {
		if _, err := st.CreateRepoIndexRow(ctx, store.CreateRepoIndexRowParams{
			RepositoryID:   repoID,
			File:           file,
			Symbol:         s.Symbol,
			Kind:           s.Kind,
			StartLine:      s.StartLine,
			EndLine:        s.EndLine,
			Column7:        vecLiteral(vecs[i]),
			EmbeddingModel: model,
			Dims:           dims,
			Column10:       s.Imports, // imports file-level (param posicional generado); nil OK
			FileHash:       hash,
		}); err != nil {
			return fmt.Errorf("insertando %s/%s en el índice: %w", file, s.Symbol, err)
		}
	}
	return nil
}

// fileHash es el SHA-256 del contenido del archivo (hex): la huella del
// resume-skip (§9.6). Las rutas del contrato son relativas al workdir; una
// absoluta o con escape (`..`) no es del clon — se rechaza (defensa en el
// límite de confianza: los nombres vienen del repo analizado).
func fileHash(workdir, file string) (string, error) {
	if filepath.IsAbs(file) || strings.HasPrefix(filepath.Clean(file), "..") {
		return "", fmt.Errorf("ruta fuera del clon: %s", file)
	}
	raw, err := os.ReadFile(filepath.Join(workdir, filepath.FromSlash(file)))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// vecLiteral serializa []float32 al literal de pgvector '[0.1,0.2,...]'
// (§3.3): misma forma del helper riVecLiteral de store (6 líneas — no
// justifica exportar algo de store solo para esto).
func vecLiteral(v []float32) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(float64(x), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
