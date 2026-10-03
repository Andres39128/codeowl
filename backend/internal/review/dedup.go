// Dedup de hallazgos por huella (guía §3.6.3): un push que corre líneas no
// republica el mismo hallazgo. Con AST (§6 F4) el ancla es el símbolo
// contenedor y, sin símbolo, la línea con tolerancia de drift (±N, config).
package review

import (
	"context"
	"strconv"
	"strings"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Fingerprint es la huella del hallazgo (§3.6.3): file + categoría + ancla.
// El ancla es la resuelta para el hallazgo — nombre del símbolo contenedor
// (§6 F4) o línea — y también el valor que se persiste en comments_sent.anchor
// de las filas inline y la clave de dedup dentro de una misma corrida.
func Fingerprint(file, category, anchor string) string {
	return file + "|" + category + "|" + anchor
}

// SymbolExtractor es la capacidad opcional del analyzer (§6 F4): extraer los
// símbolos del clon para anclar el dedup por símbolo contenedor.
// *analyze.Runner la satisface; los stubs de test no la implementan y el
// pipeline cae a anclas por línea sin fallar jamás la revisión.
type SymbolExtractor interface {
	ExtractSymbols(ctx context.Context, workdir string) ([]analyze.Symbol, error)
}

// anchorFor resuelve el ancla del hallazgo (§6 F4): el símbolo contenedor —
// mismo archivo con StartLine ≤ línea ≤ EndLine — eligiendo el MÁS INTERNO
// (menor span) cuando hay anidamiento. Sin contenedor cae a la línea
// (legacy, §3.6.3): el comportamiento numérico de siempre.
func anchorFor(f Finding, symbols []analyze.Symbol) (anchor string, isSymbol bool) {
	best, bestSpan := -1, int64(0)
	for i, s := range symbols {
		if s.File != f.File || f.Line < s.StartLine || f.Line > s.EndLine {
			continue
		}
		span := int64(s.EndLine) - int64(s.StartLine)
		if best == -1 || span < bestSpan {
			best, bestSpan = i, span
		}
	}
	if best == -1 {
		return strconv.FormatInt(int64(f.Line), 10), false
	}
	return symbols[best].Symbol, true
}

// IsDuplicate reporta si el finding ya fue comentado en una corrida
// anterior (§3.6.3/§6 F4, modo dual según el ancla resuelta del finding):
//   - ancla simbólica (no numérica): matchea filas con el MISMO símbolo
//     (igualdad exacta) y — transición sin republish masivo — filas
//     numéricas legacy dentro del drift de líneas.
//   - ancla numérica (fallback sin símbolo) o vacía: solo drift numérico,
//     el comportamiento de siempre; las filas simbólicas jamás matchean
//     (kinds de ancla distintos: miss honesto, sin log).
func IsDuplicate(existing []store.CommentsSent, f Finding, anchor string, drift int) bool {
	symbolQuery := anchor != ""
	if symbolQuery {
		if _, err := strconv.Atoi(strings.TrimSpace(anchor)); err == nil {
			symbolQuery = false // ancla numérica: modo legacy
		}
	}
	for _, c := range existing {
		if !c.File.Valid || c.File.String != f.File {
			continue
		}
		if !c.Category.Valid || c.Category.String != f.Category {
			continue
		}
		if !c.Anchor.Valid {
			continue // fila sin ancla: no deduplica
		}
		row := strings.TrimSpace(c.Anchor.String)
		if old, err := strconv.Atoi(row); err == nil {
			// Fila numérica (legacy): deduplica por drift, venga la
			// consulta simbólica (transición §6 F4) o numérica (siempre).
			if absDiff(int(f.Line), old) <= drift {
				return true
			}
			continue
		}
		// Fila simbólica: solo igualdad exacta desde una consulta simbólica.
		if symbolQuery && row == anchor {
			return true
		}
	}
	return false
}

func absDiff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
