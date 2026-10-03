// Risk scoring del tail de Run (§6 F5, T3): proxy 0-100 computable que
// alimenta el ORDEN de triage del dashboard — guía §1.1: NO es veredicto
// bloqueante ni cambia el estado de la corrida. Nada ejecuta tests (§6 F5):
// todo se deriva del diff y de los hallazgos publicados de la corrida.
//
// Fórmula transparente = suma de cuatro aportes, clampeada a [0, 100]:
//
//	sizeScore        0-35  bandas por líneas +/- del diff (ver sizeScore)
//	sensitiveScore   0-25  min(hits de archivos sensibles, 5) × 5
//	findingsScore    0-30  high×12 + medium×5 + low×2, tope 30
//	testShareScore   0-10  diffs > 50 líneas: round((1 − proporción tests) × 10)
//
// Los pesos son juicios de diseño — se ajustan acá, en un solo lugar. SOLO
// los patrones de archivos sensibles son config (RISK_SENSITIVE_PATHS).
package review

import (
	"math"
	"strings"

	"github.com/Andres39128/codeowl/backend/internal/repoconfig"
)

// Pesos y bandas del score (§6 F5): máximos 35 + 25 + 30 + 10 = 100.
const (
	// Bandas de tamaño (líneas +/- cambiadas): < = límite → puntaje.
	riskSizeTinyMax   = 50
	riskSizeSmallMax  = 200
	riskSizeMediumMax = 600
	riskSizeLargeMax  = 1500

	riskSizeTiny   = 5  // 1-50 líneas: toque quirúrgico
	riskSizeSmall  = 15 // 51-200
	riskSizeMedium = 25 // 201-600
	riskSizeLarge  = 30 // 601-1500
	riskSizeHuge   = 35 // > 1500: reescriba

	riskSensitiveEach = 5  // puntos por archivo sensible distinto tocado
	riskSensitiveCap  = 25 // 5 hits saturan

	riskFindingsHighEach   = 12 // hallazgos PUBLICADOS: lo que el bot comenta
	riskFindingsMediumEach = 5
	riskFindingsLowEach    = 2
	riskFindingsCap        = 30 // 3 highs saturan

	riskTestShareMax      = 10 // diff grande sin tests → tope del castigo
	riskTestShareMinLines = 50 // diffs más chicos no se castigan por no tocar tests
)

// RiskInput es el insumo del score, todo disponible en el tail de Run.
type RiskInput struct {
	ChangedLines  int            // líneas +/- del diff (splitDiffByFile)
	Files         []string       // archivos del diff que la corrida examinó
	TestFiles     int            // cuántos de Files son de prueba (IsTestFile)
	SensitiveHits int            // archivos sensibles distintos tocados
	Counts        map[string]int // hallazgos publicados por severidad (high/medium/low)
}

// ComputeRisk aplica la fórmula documentada arriba. Zero-value → 0; entradas
// negativas se clampan — la config o los datos podridos nunca producen un
// score inválido.
func ComputeRisk(in RiskInput) int {
	score := sizeScore(in.ChangedLines) +
		min(in.SensitiveHits, riskSensitiveCap/riskSensitiveEach)*riskSensitiveEach +
		findingsScore(in.Counts) +
		testShareScore(in)
	return min(100, max(0, score))
}

// sizeScore puntúa el tamaño del diff por bandas simples y documentadas:
// 0 → 0; 1-50 → 5; 51-200 → 15; 201-600 → 25; 601-1500 → 30; > 1500 → 35.
func sizeScore(lines int) int {
	switch {
	case lines <= 0:
		return 0
	case lines <= riskSizeTinyMax:
		return riskSizeTiny
	case lines <= riskSizeSmallMax:
		return riskSizeSmall
	case lines <= riskSizeMediumMax:
		return riskSizeMedium
	case lines <= riskSizeLargeMax:
		return riskSizeLarge
	default:
		return riskSizeHuge
	}
}

// findingsScore pondera el conjunto publicado (high pesa más) con tope: una
// revisión catastrófica no debe saturar el score sola.
func findingsScore(counts map[string]int) int {
	s := max(0, counts["high"])*riskFindingsHighEach +
		max(0, counts["medium"])*riskFindingsMediumEach +
		max(0, counts["low"])*riskFindingsLowEach
	return min(s, riskFindingsCap)
}

// testShareScore castiga la falta relativa de tests SOLO en diffs grandes:
// un diff chico sin tests es normal (README, typo); uno de 400 líneas que no
// toca tests sí es señal. Mayormente tests → 0.
func testShareScore(in RiskInput) int {
	if in.ChangedLines <= riskTestShareMinLines || len(in.Files) == 0 {
		return 0
	}
	ratio := min(1, max(0, float64(in.TestFiles)/float64(len(in.Files))))
	return int(math.Round((1 - ratio) * riskTestShareMax))
}

// IsTestFile es la heurística compartida de archivo de prueba (§6 F5):
// sufijos *_test.go/_test.py/_test.js, *.test.ts(x), *.spec.ts(x), o
// cualquier segmento de la ruta igual a test/tests.
func IsTestFile(path string) bool {
	base := path[strings.LastIndex(path, "/")+1:]
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, "_test.py"),
		strings.HasSuffix(base, "_test.js"),
		strings.HasSuffix(base, ".test.ts"),
		strings.HasSuffix(base, ".test.tsx"),
		strings.HasSuffix(base, ".spec.ts"),
		strings.HasSuffix(base, ".spec.tsx"):
		return true
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "test" || seg == "tests" {
			return true
		}
	}
	return false
}

// SensitiveHits cuenta los archivos que matchean algún patrón sensible,
// con el MISMO matcher de glob que path_filters (repoconfig.MatchPath —
// `**` cruza segmentos, `!pat` excluye). Sin patrones no hay bonus: acá el
// vacío significa "desactivado", no "pasa todo" como en path_filters.
func SensitiveHits(files, patterns []string) int {
	if len(patterns) == 0 {
		return 0
	}
	n := 0
	for _, f := range files {
		if repoconfig.MatchPath(patterns, f) {
			n++
		}
	}
	return n
}
