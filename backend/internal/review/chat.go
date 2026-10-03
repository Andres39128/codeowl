// Agente Chat (mapa: backend.review — F2, guía §6 F2/§3.5): responde
// menciones @bot en comentarios del PR. El webhook jamás llama LLM (§3.5):
// HandleChat corre dentro del ChatJob — parsea el comando, llama al gateway
// con el prompt embebido y publica la respuesta en el hilo vía PostReply.
// La idempotencia es el comentario padre (comments_sent, §3.3): un reintento
// del job no duplica la respuesta.
package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
	"github.com/Andres39128/codeowl/backend/prompts"
)

// promptChat se carga una vez al iniciar el paquete (§9.5: los prompts son
// código — jamás strings hardcodeados en Go).
var promptChat = prompts.Chat()

// defaultChatLanguage es el idioma de reserva si el repo no configuró uno
// (migración: language default 'es', §3.3).
const defaultChatLanguage = "es"

// chatLanguagePlaceholder es el hueco del system prompt que el idioma del
// repo llena.
const chatLanguagePlaceholder = "{{LANGUAGE}}"

// chatUserMarker marca el prompt de usuario del chat: enruta la respuesta
// del gateway en los stubs de test (mismo criterio que summarizerMarker).
const chatUserMarker = "Comentario del usuario en el pull request:"

// Comandos de la lista cerrada (§9.5): cualquier otro texto es chat general.
const (
	cmdReview  = "review"
	cmdTests   = "tests"
	cmdExplain = "explain"
)

// defaultChatDiffMaxLines es el tope de diff que entra al prompt del chat
// (§9.6: presupuesto propio del chat — un diff de 10MB no viaja al LLM).
const defaultChatDiffMaxLines = 5000

// defaultTestMaxSnippets es el tope de pruebas que genera /tests (§9.6:
// presupuesto propio — un PR de 50 hallazgos no genera 50 pruebas).
const defaultTestMaxSnippets = 3

// testsSinHallazgos es la respuesta de /tests sin revisión previa: sin
// hallazgos no hay contra qué generar pruebas.
const testsSinHallazgos = "No hay hallazgos de una revisión previa en este pull request: corré /review y volvé a pedirme /tests."

// closedRefusal es la negativa de /review sobre un PR cerrado (§3.6: una
// corrida sobre un PR cerrado terminaría stale quemando LLM — se responde
// sin encolar).
const closedRefusal = "Este pull request está cerrado: no encolo una revisión. Los demás comandos siguen operativos si querés seguir charlando."

// ChatInput es la identidad de la respuesta de chat: el comentario padre es
// la unidad de idempotencia (§3.3/§9.12).
type ChatInput struct {
	PullRequestID   int64
	RepositoryID    int64
	ParentCommentID string // comentario que disparó el chat
	CommentBody     string // texto del comentario del usuario
	CommentAuthor   string
	HeadSHA         string
	BaseSHA         string
	Language        string // idioma configurado del repo (§3.3)
}

// ChatResult es el desenlace para el caller (ChatJobWorker): EnqueueReview
// le ordena encolar el ReviewJob con la identidad ACTUAL del PR (§3.6).
type ChatResult struct {
	Response      string
	EnqueueReview bool
}

// ChatConfig son los topes propios del chat (§9.6: presupuesto por comando).
type ChatConfig struct {
	DiffMaxLines    int // tope de líneas de diff que entran al prompt
	TestMaxSnippets int // tope de pruebas generadas por /tests (§9.6)
}

// DefaultChatConfig devuelve la config por defecto (espejo de
// config.DefaultChatDiffMaxLines y DefaultChatTestMaxSnippets — §4.2).
func DefaultChatConfig() ChatConfig {
	return ChatConfig{DiffMaxLines: defaultChatDiffMaxLines, TestMaxSnippets: defaultTestMaxSnippets}
}

// normalized clampea los valores fuera de rango a los defaults (mismo
// criterio que Config.normalized).
func (c ChatConfig) normalized() ChatConfig {
	if c.DiffMaxLines < 1 {
		c.DiffMaxLines = defaultChatDiffMaxLines
	}
	if c.TestMaxSnippets < 1 {
		c.TestMaxSnippets = defaultTestMaxSnippets
	}
	return c
}

// HandleChat ejecuta una respuesta de chat (§6 F2). Devuelve error solo ante
// fallos de infraestructura reintentables (BD, diff, gateway) — el job
// reintenta y la idempotencia evita duplicar la respuesta ya publicada.
func HandleChat(ctx context.Context, cfg ChatConfig, st Store, gw Gateway, provider vcs.VCSProvider, input ChatInput) (*ChatResult, error) {
	cfg = cfg.normalized()

	// Idempotencia (§9.12): una respuesta ya publicada para este comentario
	// padre corta acá — sin LLM, sin publicación, sin re-encolar.
	sent, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: input.PullRequestID,
		Type:          "chat",
	})
	if err != nil {
		return nil, fmt.Errorf("cargando comments_sent del PR: %w", err)
	}
	for _, row := range sent {
		if row.ParentCommentID.Valid && row.ParentCommentID.String == input.ParentCommentID {
			slog.Info("chat: respuesta ya publicada para este comentario, el reintento no hace nada",
				"parent", input.ParentCommentID)
			return &ChatResult{}, nil
		}
	}

	repo, err := st.GetRepository(ctx, input.RepositoryID)
	if err != nil {
		return nil, fmt.Errorf("cargando el repo %d: %w", input.RepositoryID, err)
	}
	pr, err := st.GetPullRequest(ctx, input.PullRequestID)
	if err != nil {
		return nil, fmt.Errorf("cargando el PR %d: %w", input.PullRequestID, err)
	}

	cmd, question := parseCommand(input.CommentBody)

	// /review no responde: el worker encola el ReviewJob con la identidad
	// actual (mapa: flujo chatear_con_bot). Sobre un PR cerrado responde la
	// negativa sin encolar (§3.6) — la negativa sí se registra: el reintento
	// no la duplica.
	if cmd == cmdReview {
		if pr.State != "open" {
			if err := postChat(ctx, provider, st, &repo, &pr, input, closedRefusal); err != nil {
				return nil, err
			}
			return &ChatResult{Response: closedRefusal}, nil
		}
		return &ChatResult{EnqueueReview: true}, nil
	}

	// Contexto: el diff del PR con el tope propio del chat (§9.6).
	diff, err := provider.GetDiff(ctx, &repo, &pr)
	if err != nil {
		return nil, fmt.Errorf("obteniendo el diff del PR %d: %w", pr.Number, err)
	}
	diff = truncateLines(diff, cfg.DiffMaxLines)

	// /tests genera una prueba por hallazgo de la última revisión (F4):
	// ilustrativas, en el hilo — jamás inline aplicable.
	if cmd == cmdTests {
		resp, err := handleTests(ctx, cfg, st, gw, provider, input, &repo, &pr, diff)
		if err != nil {
			return nil, err
		}
		return &ChatResult{Response: resp}, nil
	}

	language := input.Language
	if language == "" {
		language = defaultChatLanguage
	}
	system := strings.ReplaceAll(promptChat, chatLanguagePlaceholder, language)

	resp, err := gw.Complete(ctx, roleReview, system, chatUser(question, cmd, diff))
	if err != nil {
		return nil, fmt.Errorf("failover agotado: %w", err)
	}

	if err := postChat(ctx, provider, st, &repo, &pr, input, resp); err != nil {
		return nil, err
	}
	return &ChatResult{Response: resp}, nil
}

// parseCommand extrae el comando del primer token útil del comentario (§9.5:
// lista cerrada): salta las menciones @usuario iniciales — la del bot
// incluida — y devuelve ("comando", resto) si el primer token es /comando;
// ("", texto) si es chat general. El comando sale en minúsculas.
func parseCommand(body string) (cmd, question string) {
	fields := strings.Fields(body)
	i := 0
	for i < len(fields) && strings.HasPrefix(fields[i], "@") {
		i++
	}
	if i < len(fields) && strings.HasPrefix(fields[i], "/") {
		return strings.ToLower(strings.TrimPrefix(fields[i], "/")), strings.Join(fields[i+1:], " ")
	}
	return "", strings.Join(fields[i:], " ")
}

// handleTests ejecuta /tests (F4): una prueba unitaria por hallazgo de la
// última revisión, hasta el tope cfg.TestMaxSnippets (§9.6). Los hallazgos de
// archivos sin framework conocido se saltan; si ninguno genera prueba, se
// responde igual — jamás silencio. La respuesta va al hilo (PostReply).
func handleTests(ctx context.Context, cfg ChatConfig, st Store, gw Gateway, provider vcs.VCSProvider, input ChatInput, repo *store.Repository, pr *store.PullRequest, diff string) (string, error) {
	rev, err := st.GetLatestReviewByPR(ctx, input.PullRequestID)
	if errors.Is(err, pgx.ErrNoRows) {
		if perr := postChat(ctx, provider, st, repo, pr, input, testsSinHallazgos); perr != nil {
			return "", perr
		}
		return testsSinHallazgos, nil
	}
	if err != nil {
		return "", fmt.Errorf("buscando la última review del PR %d: %w", input.PullRequestID, err)
	}
	rows, err := st.ListFindingsByReview(ctx, rev.ID)
	if err != nil {
		return "", fmt.Errorf("cargando los hallazgos de la review %d: %w", rev.ID, err)
	}

	hunks, _, _ := splitDiffByFile(diff)
	var snippets strings.Builder
	n := 0
	for _, r := range rows {
		if n >= cfg.TestMaxSnippets {
			break
		}
		fw, ok := frameworkForFile(r.File)
		if !ok {
			continue // extensión sin framework conocido: no se inventa
		}
		code, err := runTestGenerator(ctx, gw, Finding{
			File: r.File, Line: r.Line, Severity: r.Severity,
			Category: r.Category, Body: r.Body, Suggestion: r.Suggestion.String,
		}, hunks[r.File], fw)
		if err != nil {
			if ctx.Err() != nil {
				return "", err // el job está muriendo: el reintento regenera
			}
			slog.Warn("review: /tests sin prueba para un hallazgo", "archivo", r.File, "error", err)
			continue
		}
		n++
		fmt.Fprintf(&snippets, "\n**%s:%d** · %s · %s\n\n%s\n", r.File, r.Line, severityLabel[r.Severity], r.Category, code)
	}

	body := "### Pruebas sugeridas\n"
	if n > 0 {
		body += "\nPruebas que reproducen los hallazgos de la última revisión (ilustrativas — no se aplican con un clic):\n" + snippets.String()
	} else {
		body += "\nNo pude generar pruebas para los hallazgos de la última revisión."
	}
	if err := postChat(ctx, provider, st, repo, pr, input, body); err != nil {
		return "", err
	}
	return body, nil
}

// frameworkByExt mapea la extensión del archivo cambiado al framework de
// testing que usa el generador de pruebas (F4: detección por extensión — lo
// fino con go-enry llega si algún día hace falta).
var frameworkByExt = map[string]string{
	".go": "go test", ".ts": "vitest", ".tsx": "vitest", ".py": "pytest", ".rs": "cargo test",
}

// frameworkForFile deduce el framework de testing por extensión del archivo
// del hallazgo. false = extensión sin framework conocido: esa prueba no se
// genera.
func frameworkForFile(file string) (string, bool) {
	for ext, fw := range frameworkByExt {
		if strings.HasSuffix(file, ext) {
			return fw, true
		}
	}
	return "", false
}

// chatUser arma el prompt de usuario del chat: el comentario del usuario con
// su pedido (según el comando) sobre el diff del PR como contexto.
func chatUser(question, cmd, diff string) string {
	var b strings.Builder
	if question != "" {
		b.WriteString(chatUserMarker + " " + question + "\n\n")
	} else {
		b.WriteString(chatUserMarker + " (solo la mención al bot)\n\n")
	}
	switch cmd {
	case cmdExplain:
		b.WriteString("Pedido: explicá el diff (o el punto que el comentario mencione) en lenguaje llano.\n\n")
	default:
		b.WriteString("Pedido: respondé el comentario como asistente de revisión de código.\n\n")
	}
	b.WriteString("Diff del pull request:\n\n```diff\n" + diff + "```")
	return b.String()
}

// truncateLines corta el diff al tope de líneas del chat (§9.6: nada se
// recorta en silencio — el corte queda declarado dentro del prompt).
func truncateLines(diff string, max int) string {
	lines := strings.Split(diff, "\n")
	if len(lines) <= max {
		return diff
	}
	return strings.Join(lines[:max], "\n") +
		fmt.Sprintf("\n... (diff truncado al tope de %d líneas del chat, §9.6)", max)
}

// postChat publica la respuesta en el hilo del comentario padre (PostReply)
// y registra comments_sent con el padre — la marca de idempotencia (§3.3).
func postChat(ctx context.Context, provider vcs.VCSProvider, st Store, repo *store.Repository, pr *store.PullRequest, input ChatInput, body string) error {
	id, err := provider.PostReply(ctx, repo, pr, input.ParentCommentID, maskSecrets(body))
	if err != nil {
		return fmt.Errorf("publicando la respuesta en el hilo: %w", err)
	}
	if _, err := st.CreateCommentSent(ctx, store.CreateCommentSentParams{
		PullRequestID:   input.PullRequestID,
		CommentID:       id,
		Type:            "chat",
		ParentCommentID: pgText(input.ParentCommentID),
	}); err != nil {
		return fmt.Errorf("registrando la respuesta de chat: %w", err)
	}
	return nil
}
