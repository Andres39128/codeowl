// Mapeo de severidad y categoría al conjunto cerrado de findings.severity /
// findings.category (§3.3: high|medium|low, security|logic|performance|
// style|tests|other). Cada linter tiene su propia escala: la normalización
// vive acá, en un solo lugar y determinística.
package main

import "strings"

// redactedMask reemplaza al valor detectado en cualquier cuerpo de hallazgo
// de Gitleaks (§9.4: solo ruta, línea y regla sobreviven).
const redactedMask = "[REDACTED]"

// maskSecret elimina del cuerpo toda ocurrencia del valor detectado (y del
// match crudo, que incluye contexto). Defensa en profundidad: el reporte ya
// sale con --redact y el parseo nunca copia Secret a la salida.
func maskSecret(body string, values ...string) string {
	for _, v := range values {
		if v != "" && strings.Contains(body, v) {
			body = strings.ReplaceAll(body, v, redactedMask)
		}
	}
	return body
}

// --- ESLint: severity 2=error→high, 1=warning→medium, resto descartado -------

// severityFromESLint mapea el nivel numérico de ESLint; debajo de warning el
// hallazgo queda en low (el contrato no descarta niveles, los degrada).
func severityFromESLint(sev int) string {
	switch sev {
	case 2:
		return "high"
	case 1:
		return "medium"
	default:
		return "low"
	}
}

// eslintSecurity / eslintPerformance / eslintLogic / eslintStyle: reglas core
// clasificadas a mano; lo no listado cae en "other" (contrato del pipeline).
var (
	eslintSecurity    = []string{"no-eval", "no-new-func"}
	eslintPerformance = []string{"no-await-in-loop"}
	eslintLogic       = []string{"complexity", "max-depth", "max-lines-per-function", "no-unreachable", "no-fallthrough", "eqeqeq", "no-cond-assign", "default-case", "no-constant-condition"}
	eslintStyle       = []string{"no-unused-vars", "camelcase", "quotes", "semi", "indent", "no-var", "prefer-const", "max-len", "no-trailing-spaces", "eol-last", "comma-dangle", "brace-style", "no-tabs", "no-mixed-spaces-and-tabs", "spaced-comment", "no-extra-semi", "curly"}
)

func classifyESLint(rule string) string {
	return classifyBySets(rule, eslintSecurity, eslintPerformance, eslintLogic, eslintStyle)
}

// classifyBySets mapea regla → security/performance/logic/style; fallback other.
func classifyBySets(rule string, security, performance, logic, style []string) string {
	for _, r := range security {
		if rule == r {
			return "security"
		}
	}
	for _, r := range performance {
		if rule == r {
			return "performance"
		}
	}
	for _, r := range logic {
		if rule == r {
			return "logic"
		}
	}
	for _, r := range style {
		if rule == r {
			return "style"
		}
	}
	return "other"
}

// --- Ruff: el JSON no trae severidad, se deriva del código de regla -----------
//
// E9 (errores de sintaxis/ejecución) → high; S (bandit, secrets/inseguridad)
// → high; F (pyflakes: nombres sin definir, etc.) → medium; el resto → low.

func ruffSeverity(code string) string {
	switch {
	case strings.HasPrefix(code, "E9"), strings.HasPrefix(code, "S"):
		return "high"
	case strings.HasPrefix(code, "F"), strings.HasPrefix(code, "B"):
		return "medium"
	default:
		return "low"
	}
}

func classifyRuff(code string) string {
	switch {
	case strings.HasPrefix(code, "S"):
		return "security"
	case strings.HasPrefix(code, "PERF"):
		return "performance"
	case strings.HasPrefix(code, "C9"), strings.HasPrefix(code, "B"), strings.HasPrefix(code, "F8"):
		return "logic"
	case strings.HasPrefix(code, "E"), strings.HasPrefix(code, "W"), strings.HasPrefix(code, "I"), strings.HasPrefix(code, "Q"):
		return "style"
	default:
		return "other"
	}
}

// --- golangci-lint: severity propio si es válido; si no, medium ---------------
//
// Categoría: gosec → security; linters de formato → style; fallback other
// (el veredicto de fondo lo hace el Verifier, no la normalización).

func golangciSeverity(sev string) string {
	switch strings.ToLower(sev) {
	case "high", "error":
		return "high"
	case "low":
		return "low"
	default:
		return "medium"
	}
}

var (
	golangciSecurity = []string{"gosec"}
	golangciStyle    = []string{"gofmt", "gofumpt", "goimports", "lll", "dupl", "whitespace", "wsl"}
)

func classifyGolangCI(linter string) string {
	for _, r := range golangciSecurity {
		if linter == r {
			return "security"
		}
	}
	for _, r := range golangciStyle {
		if linter == r {
			return "style"
		}
	}
	return "other"
}

// --- Clippy: error→high, warning→medium, note/help→low ------------------------

func clippySeverity(level string) string {
	switch level {
	case "error":
		return "high"
	case "warning":
		return "medium"
	default:
		return "low"
	}
}

func classifyClippy(rule string) string {
	switch {
	case strings.HasPrefix(rule, "clippy::perf"):
		return "performance"
	case strings.HasPrefix(rule, "clippy::style"), strings.HasPrefix(rule, "clippy::pedantic"):
		return "style"
	case strings.HasPrefix(rule, "clippy::correctness"), strings.HasPrefix(rule, "clippy::suspicious"), strings.HasPrefix(rule, "clippy::complexity"):
		return "logic"
	default:
		return "other"
	}
}

// envWith reemplaza (o agrega) clave=valor en un entorno base. Es necesario
// porque exec no deduplica y getenv del hijo devuelve la PRIMERA ocurrencia:
// un append simple sobre os.Environ() no pisa variables existentes.
func envWith(base []string, vars ...string) []string {
	prefix := func(entry, key string) bool {
		return strings.HasPrefix(entry, key+"=")
	}
	out := make([]string, 0, len(base)+len(vars))
	for _, v := range vars {
		out = append(out, v)
	}
	for _, e := range base {
		key, _, _ := strings.Cut(e, "=")
		duplicated := false
		for _, v := range vars {
			if prefix(v, key) {
				duplicated = true
				break
			}
		}
		if !duplicated {
			out = append(out, e)
		}
	}
	return out
}
