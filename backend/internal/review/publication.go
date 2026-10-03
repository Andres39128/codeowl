// Publicación en dos fases (guía §3.6.2/§6): fase 1 publica el resumen
// único edit-in-place; fase 2 publica los comentarios inline y re-edita el
// resumen con el recuento final por severidad, los hallazgos que bajaron al
// resumen y la declaración de cobertura (§9.6). El enmascarado de secrets
// (§9.4) vive acá — el publicador — y aplica a todo comentario, venga de
// Gitleaks o citado por un agente LLM.
package review

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/store"
	"github.com/Andres39128/codeowl/backend/internal/vcs"
)

// publisher publica contra el VCS y registra comments_sent (idempotencia y
// actualización del resumen, §3.6.2).
type publisher struct {
	vcs      vcs.VCSProvider
	repo     *store.Repository
	pr       *store.PullRequest
	st       Store
	prID     int64
	reviewID int64
	summary  *store.CommentsSent // fila del resumen; nil si nunca se publicó
}

// newPublisher carga la fila del resumen previo (si existe): PostSummary
// edita in place sobre su comment_id o crea uno nuevo si fue borrado (404).
func newPublisher(ctx context.Context, provider vcs.VCSProvider, repo *store.Repository, pr *store.PullRequest, st Store, prID, reviewID int64) (*publisher, error) {
	rows, err := st.GetCommentsSentByPRAndType(ctx, store.GetCommentsSentByPRAndTypeParams{
		PullRequestID: prID,
		Type:          "summary",
	})
	if err != nil {
		return nil, err
	}
	p := &publisher{vcs: provider, repo: repo, pr: pr, st: st, prID: prID, reviewID: reviewID}
	if len(rows) > 0 {
		p.summary = &rows[0]
	}
	return p, nil
}

// publishSummary publica (o re-edita) el resumen único del PR y sincroniza
// comments_sent: crea la fila la primera vez; si el VCS devolvió un ID
// distinto (comentario recreado tras borrado manual), lo actualiza (§3.6.2).
func (p *publisher) publishSummary(ctx context.Context, body string) error {
	existing := ""
	if p.summary != nil {
		existing = p.summary.CommentID
	}
	id, err := p.vcs.PostSummary(ctx, p.repo, p.pr, maskSecrets(body), existing)
	if err != nil {
		return err
	}
	if p.summary == nil {
		row, err := p.st.CreateCommentSent(ctx, store.CreateCommentSentParams{
			PullRequestID: p.prID,
			ReviewID:      pgtype.Int8{Int64: p.reviewID, Valid: true},
			CommentID:     id,
			Type:          "summary",
		})
		if err != nil {
			return err
		}
		p.summary = &row
		return nil
	}
	if id != p.summary.CommentID {
		if _, err := p.st.UpdateCommentSentCommentID(ctx, store.UpdateCommentSentCommentIDParams{
			ID:        p.summary.ID,
			CommentID: id,
		}); err != nil {
			return err
		}
		p.summary.CommentID = id
	}
	return nil
}

// publishInline publica el comentario inline del finding en su posición del
// diff y registra la fila inline con su huella (dedup de corridas futuras,
// §3.6.3). Con sugerencia va por PostSuggestion (aplicable con un clic);
// sin ella, PostInlineComment (comentario plano).
func (p *publisher) publishInline(ctx context.Context, f Finding, pos vcs.CommentPosition) error {
	body := maskSecrets(inlineBody(f))
	var (
		id  string
		err error
	)
	if f.Suggestion != "" {
		id, err = p.vcs.PostSuggestion(ctx, p.repo, p.pr, pos, body)
	} else {
		id, err = p.vcs.PostInlineComment(ctx, p.repo, p.pr, pos, body)
	}
	if err != nil {
		return err
	}
	_, err = p.st.CreateCommentSent(ctx, store.CreateCommentSentParams{
		PullRequestID: p.prID,
		ReviewID:      pgtype.Int8{Int64: p.reviewID, Valid: true},
		CommentID:     id,
		Type:          "inline",
		File:          pgtype.Text{String: f.File, Valid: true},
		Category:      pgtype.Text{String: f.Category, Valid: true},
		Anchor:        pgtype.Text{String: strconv.FormatInt(int64(f.Line), 10), Valid: true},
	})
	return err
}

// severityLabel mapea la severidad al rótulo del tema (§5.1: severidad =
// color + ícono + texto — el texto manda).
var severityLabel = map[string]string{
	"high": "Alta", "medium": "Media", "low": "Baja",
}

// inlineBody arma el cuerpo del comentario inline: severidad + categoría +
// cuerpo y, si hay sugerencia, el bloque aplicable con un clic (publicado
// vía PostSuggestion). El bloque ```suggestion``` contiene SOLO el código
// corregido — sin markdown ni explicación: es lo que el VCS inserta al
// aplicar la sugerencia.
func inlineBody(f Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s · %s**\n\n%s", severityLabel[f.Severity], f.Category, f.Body)
	if f.Suggestion != "" {
		b.WriteString("\n\n```suggestion\n" + f.Suggestion + "\n```")
	}
	return b.String()
}

// redacted reemplaza cualquier valor que parezca un secret.
const redacted = "[redacted]"

// secretPatterns detecta los formatos de token más comunes.
// ponytail: lista corta de patrones de alta confianza — los falsos
// positivos enmascaran texto legítimo; ampliar solo con evidencia.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),                                                      // AWS access key
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{36}\b`),                                                   // GitHub PAT clásico
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}\b`),                                          // GitHub PAT fino
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),                                                 // OpenAI-style
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),                                          // Slack
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),                                                 // Google API key
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`), // PEM
}

// secretAssignRe enmascara asignaciones a credenciales conservando el nombre
// de la clave: `password: hunter2` → `password: [redacted]`.
var secretAssignRe = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key)(\s*[:=]\s*)["']?[^\s"']{8,}`)

// maskSecrets enmascara valores de secrets en el cuerpo de un comentario
// (§9.4): publicar el valor lo duplicaría en un lugar público — queda solo
// en la evidencia interna. Aplica a inline, resumen y chat.
func maskSecrets(body string) string {
	body = secretAssignRe.ReplaceAllString(body, "${1}${2}"+redacted)
	for _, re := range secretPatterns {
		body = re.ReplaceAllString(body, redacted)
	}
	return body
}

// summaryHeader es el encabezado fijo del comentario de resumen.
const summaryHeader = "## 🦉 Revisión automática"

// provisionalSummary arma el cuerpo de fase 1 (§3.6.2): provisional por
// diseño — la re-edición final suma recuento y cobertura.
func provisionalSummary(s *SummaryResult) string {
	return summaryParts(s, nil, nil, "")
}

// finalSummary arma el cuerpo de la re-edición final (§3.6.2): recuento por
// severidad, hallazgos fuera del diff, declaración de cobertura (§9.6) y,
// en fallo, el estado final (§9.7). La cobertura siempre se declara —
// completa o parcial, nunca se calla.
func finalSummary(s *SummaryResult, counts map[string]int, outOfDiff []Finding, coverage []string, failure string) string {
	if coverage == nil {
		coverage = []string{}
	}
	return summaryParts(s, outOfDiff, coverage, failure) + countsSection(counts)
}

// summaryParts arma el cuerpo común: encabezado, resumen, walkthrough y
// mermaid validado; más las secciones de detalle que correspondan.
func summaryParts(s *SummaryResult, outOfDiff []Finding, coverage []string, failure string) string {
	var b strings.Builder
	b.WriteString(summaryHeader + "\n\n")
	b.WriteString(s.Summary)
	if s.Walkthrough != "" {
		b.WriteString("\n\n" + s.Walkthrough)
	}
	if s.Mermaid != "" {
		b.WriteString("\n\n```mermaid\n" + s.Mermaid + "\n```")
	}
	if len(outOfDiff) > 0 {
		b.WriteString("\n\n### Hallazgos fuera del diff\n\n")
		b.WriteString("Líneas no visibles en el diff actual (§3.6.5):\n")
		for _, f := range outOfDiff {
			fmt.Fprintf(&b, "\n- **%s · %s** — `%s:%d`: %s",
				severityLabel[f.Severity], f.Category, f.File, f.Line, f.Body)
		}
	}
	if failure != "" {
		fmt.Fprintf(&b, "\n\n> ⚠️ **La revisión falló**: %s. Se reintenta automáticamente (§9.7).", failure)
	}
	if coverage != nil {
		if len(coverage) > 0 {
			b.WriteString("\n\n### Cobertura parcial\n\n")
			for _, c := range coverage {
				b.WriteString("\n- " + c)
			}
		} else {
			b.WriteString("\n\nCobertura completa: SAST + LLM sobre todo el diff.")
		}
	}
	b.WriteString("\n\n<sub>Resumen editado in place en cada corrida — nunca un comentario nuevo por push (§3.6.2).</sub>")
	return b.String()
}

// countsSection es el recuento por severidad de la re-edición final
// (§3.6.2: el resumen de fase 1 es provisional; este recuento lo completa).
func countsSection(counts map[string]int) string {
	return fmt.Sprintf("\n\n### Hallazgos: %d alta · %d media · %d baja",
		counts["high"], counts["medium"], counts["low"])
}
