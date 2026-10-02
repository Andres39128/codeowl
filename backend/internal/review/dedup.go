// Dedup de hallazgos por huella (guía §3.6.3): un push que corre líneas no
// republica el mismo hallazgo. El ancla es la línea con tolerancia de drift
// (±N, config) hasta que F4 la reemplace por el símbolo contenedor con AST.
package review

import (
	"strconv"
	"strings"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// Fingerprint es la huella del hallazgo (§3.6.3): file + categoría + ancla.
// También es el valor que se persiste en comments_sent.anchor de las filas
// inline, y la clave de dedup dentro de una misma corrida.
func Fingerprint(file, category string, line int32) string {
	return file + "|" + category + "|" + strconv.FormatInt(int64(line), 10)
}

// IsDuplicate reporta si el finding ya fue comentado en una corrida
// anterior: misma huella pero tolerando drift de líneas — |nueva - vieja| ≤
// drift (config, default 3). El ancla de la fila es la línea de la corrida
// que publicó; un push que corre líneas no republica.
func IsDuplicate(existing []store.CommentsSent, f Finding, drift int) bool {
	for _, c := range existing {
		if !c.File.Valid || c.File.String != f.File {
			continue
		}
		if !c.Category.Valid || c.Category.String != f.Category {
			continue
		}
		if !c.Anchor.Valid {
			continue
		}
		old, err := strconv.Atoi(strings.TrimSpace(c.Anchor.String))
		if err != nil {
			continue // ancla no numérica (formato viejo): no deduplica
		}
		if absDiff(int(f.Line), old) <= drift {
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
