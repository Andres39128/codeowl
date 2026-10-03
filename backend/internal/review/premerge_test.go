// Tests del agente Pre-merge (§6 F5): validación §9.8 de la salida, la
// llamada vía gateway con reintentos y la integración en Run — el veredicto
// es una sección de la re-edición final del resumen (§3.6), nunca una action
// del VCS (§1.1). Reusa stubStore/stubGateway/stubVCS de review_test.go.
package review

import (
	"context"
	"strings"
	"testing"

	"github.com/Andres39128/codeowl/backend/internal/repoconfig"
	"github.com/Andres39128/codeowl/backend/prompts"
)

// premergeFixture es la salida JSON válida del Pre-merge para los tests.
const premergeFixture = `{"verdict":"apto_con_observaciones","checklist":[{"item":"Sin hallazgos altos sin atender","ok":true},{"item":"Cobertura completa del diff","ok":true}],"resumen":"Riesgo bajo; el cambio es mergeable."}`

func TestParsePreMerge(t *testing.T) {
	t.Run("salida válida", func(t *testing.T) {
		pm, err := parsePreMerge(premergeFixture)
		if err != nil {
			t.Fatalf("parsePreMerge: %v", err)
		}
		if pm.Verdict != "apto_con_observaciones" || pm.Resumen == "" || len(pm.Checklist) != 2 {
			t.Fatalf("veredicto = %+v", pm)
		}
		if !pm.Checklist[0].OK || !pm.Checklist[1].OK {
			t.Errorf("checklist = %+v, querés ambos ok=true", pm.Checklist)
		}
	})

	t.Run("tolera fences de transporte", func(t *testing.T) {
		if _, err := parsePreMerge("```json\n" + premergeFixture + "\n```"); err != nil {
			t.Errorf("con fences: %v", err)
		}
	})

	t.Run("checklist vacía pasa (0-8, el prompt pide 3-6)", func(t *testing.T) {
		pm, err := parsePreMerge(`{"verdict":"apto","checklist":[],"resumen":"limpio"}`)
		if err != nil {
			t.Fatalf("checklist vacía: %v", err)
		}
		if len(pm.Checklist) != 0 {
			t.Errorf("checklist = %+v, querés vacía", pm.Checklist)
		}
	})

	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"JSON malformado", "esto no es json", "JSON inválido"},
		{"verdict fuera del conjunto cerrado", `{"verdict":"approved","checklist":[],"resumen":"x"}`, "fuera del conjunto cerrado"},
		{"verdict vacío", `{"verdict":"","checklist":[],"resumen":"x"}`, "fuera del conjunto cerrado"},
		{"resumen vacío", `{"verdict":"apto","checklist":[],"resumen":"  "}`, "resumen vacío"},
		{"checklist sobre el tope", premergeOver(9), "tope 8"},
		{"item vacío", `{"verdict":"apto","checklist":[{"item":"  ","ok":true}],"resumen":"x"}`, "item vacío"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parsePreMerge(c.content)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("parsePreMerge err = %v, querés contener %q", err, c.wantErr)
			}
		})
	}
}

// premergeOver arma un JSON con n ítems de checklist (para el tope).
func premergeOver(n int) string {
	items := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			items += ","
		}
		items += `{"item":"punto","ok":true}`
	}
	return `{"verdict":"apto","checklist":[` + items + `],"resumen":"x"}`
}

func TestRunPreMerge(t *testing.T) {
	stats := PreMergeStats{
		RiskScore:    17,
		Counts:       map[string]int{"high": 1, "medium": 0, "low": 0},
		Categories:   []string{"security"},
		Coverage:     nil,
		Status:       StatusSuccess,
		Files:        1,
		ChangedLines: 4,
		TestFiles:    0,
	}

	t.Run("happy: prompt con métricas y veredicto parseado", func(t *testing.T) {
		gw := newStubGateway()
		gw.premerge = []string{premergeFixture}
		cfg := DefaultConfig()
		rc := repoconfig.RepoConfig{Language: "fr", Profile: "assertive"}
		pm, err := runPreMerge(context.Background(), gw, cfg, rc, stats)
		if err != nil {
			t.Fatalf("runPreMerge: %v", err)
		}
		if pm.Verdict != "apto_con_observaciones" {
			t.Errorf("verdict = %q", pm.Verdict)
		}
		// El system prompt viaja con el idioma del repo lleno (§9.5).
		if got := gw.systemSeen[0]; strings.Contains(got, languagePlaceholder) || !strings.Contains(got, "fr") {
			t.Errorf("system prompt sin idioma lleno: %q", got[:min(len(got), 120)])
		}
		// El prompt de usuario trae las métricas de la corrida.
		prompt := gw.premergeSeen[0]
		for _, want := range []string{
			"Estado de la corrida: success",
			"Risk score computado: 17/100",
			"1 alta, 0 media, 0 baja",
			"Categorías de los hallazgos: security",
			"4 líneas cambiadas en 1 archivos",
			"Cobertura completa",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("al prompt del pre-merge le falta %q:\n%s", want, prompt)
			}
		}
	})

	t.Run("cobertura parcial declarada en el prompt", func(t *testing.T) {
		gw := newStubGateway()
		gw.premerge = []string{premergeFixture}
		conParcial := stats
		conParcial.Coverage = []string{"análisis SAST omitido: el analyzer falló"}
		if _, err := runPreMerge(context.Background(), gw, DefaultConfig(), repoconfig.RepoConfig{}, conParcial); err != nil {
			t.Fatalf("runPreMerge: %v", err)
		}
		if p := gw.premergeSeen[0]; !strings.Contains(p, "Cobertura parcial declarada") || !strings.Contains(p, "analyzer falló") {
			t.Errorf("el prompt no declara la cobertura parcial:\n%s", p)
		}
	})

	t.Run("malformada tras los reintentos: descarte (§9.8)", func(t *testing.T) {
		gw := newStubGateway()
		gw.premerge = []string{"no json", `{"verdict":"maybe"}`}
		cfg := DefaultConfig()
		cfg.AgentRetries = 1
		pm, err := runPreMerge(context.Background(), gw, cfg, repoconfig.RepoConfig{}, stats)
		if err == nil || pm != nil {
			t.Fatalf("querés nil + error, got (%+v, %v)", pm, err)
		}
		if gw.calls != 2 {
			t.Errorf("llamadas = %d, querés 2 (1 inicial + 1 reintento)", gw.calls)
		}
	})

	t.Run("gateway caído: failover agotado sin reintento local", func(t *testing.T) {
		gw := newStubGateway() // sin cola: Complete devuelve error
		pm, err := runPreMerge(context.Background(), gw, DefaultConfig(), repoconfig.RepoConfig{}, stats)
		if err == nil || pm != nil {
			t.Fatalf("querés nil + error, got (%+v, %v)", pm, err)
		}
		if !strings.Contains(err.Error(), "failover agotado") {
			t.Errorf("err = %v, querés failover agotado", err)
		}
		if gw.calls != 1 {
			t.Errorf("llamadas = %d, querés 1 (el fallo del gateway no se reintenta acá, §9.7)", gw.calls)
		}
	})
}

// Run-level: el veredicto computed entra en la re-edición final del resumen
// (fase 2) — la provisional de fase 1 sale sin la sección.
func TestRunPreMergeSeccionEnResumen(t *testing.T) {
	d := newDeps()
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "inyección SQL", "db.Query(q, arg)")}
	d.gw.summarizer = []string{summarizerJSON}
	d.gw.premerge = []string{premergeFixture}

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Fatalf("status = %q, querés success", res.Status)
	}
	if len(d.vcs.summaryBods) != 2 {
		t.Fatalf("PostSummary llamado %d veces, querés 2", len(d.vcs.summaryBods))
	}
	if strings.Contains(d.vcs.summaryBods[0], "Veredicto pre-merge") {
		t.Errorf("el resumen provisional no debe traer el veredicto:\n%s", d.vcs.summaryBods[0])
	}
	final := d.vcs.summaryBods[1]
	if !strings.Contains(final, "### Veredicto pre-merge: apto_con_observaciones") {
		t.Errorf("el resumen final no trae el veredicto:\n%s", final)
	}
	if !strings.Contains(final, "✅ Sin hallazgos altos sin atender") {
		t.Errorf("falta el ítem ok de la checklist:\n%s", final)
	}
	if !strings.Contains(final, "Riesgo bajo") {
		t.Errorf("falta el resumen del veredicto:\n%s", final)
	}
	if !strings.Contains(final, "no bloquea el merge") {
		t.Errorf("la sección debe declarar su carácter advisory (§1.1):\n%s", final)
	}
	// Posición: después de la cobertura, antes del recuento final (§6 F5).
	if strings.Index(final, "Veredicto pre-merge") > strings.Index(final, "### Hallazgos: ") {
		t.Errorf("el veredicto debe ir antes del recuento por severidad:\n%s", final)
	}
}

// Fallo del agente pre-merge (§9.8): descarte registrado, la corrida sigue
// success y el resumen sale sin la sección — jamás recorte silencioso de la
// corrida por un veredicto advisory.
func TestRunPreMergeFalloSinSeccion(t *testing.T) {
	d := newDeps()
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
	d.gw.summarizer = []string{summarizerJSON}
	// gw.premerge vacío: el gateway devuelve error → descarte.

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("un fallo del pre-merge no debe fallar la corrida: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, querés success", res.Status)
	}
	final := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
	if strings.Contains(final, "Veredicto pre-merge") {
		t.Errorf("sin veredicto el resumen no debe traer la sección:\n%s", final)
	}
	if !strings.Contains(final, "### Hallazgos: 1 alta") {
		t.Errorf("el resto del resumen final debe quedar intacto:\n%s", final)
	}
}

// Corrida stale (§3.6.1.3): el veredicto no se pide — ambas salidas stale
// cortan antes de la zona del veredicto y no queman una llamada LLM.
func TestRunStaleSinPreMerge(t *testing.T) {
	d := newDeps()
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
	d.gw.summarizer = []string{summarizerJSON}
	d.gw.premerge = []string{premergeFixture} // encolada a propósito: no debe consumirse
	d.st.pr.HeadSha = "headnuevo"

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusStale {
		t.Fatalf("status = %q, querés stale", res.Status)
	}
	if len(d.gw.premergeSeen) != 0 || d.gw.calls != 0 {
		t.Errorf("una corrida stale no debe pedir el veredicto (llamadas=%d, premerge=%d)",
			d.gw.calls, len(d.gw.premergeSeen))
	}
}

// Fallo de publicación inline (§9.7): la corrida falla ANTES de la zona del
// veredicto — la re-edición de fallo sale sin la sección (no hay veredicto
// computado que mostrar; el closure lo pasa tal cual: nil).
func TestRunPublishFailsSinVeredicto(t *testing.T) {
	d := newDeps()
	d.vcs.failInline = true
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
	d.gw.summarizer = []string{summarizerJSON}
	d.gw.premerge = []string{premergeFixture} // no debe consumirse

	res, err := d.run(t)
	if err == nil {
		t.Fatal("Run debería devolver el error de publicación para que el job reintente")
	}
	if res.Status != StatusFailed {
		t.Fatalf("status = %q, querés failed", res.Status)
	}
	if len(d.gw.premergeSeen) != 0 {
		t.Errorf("el veredicto no debe pedirse en una corrida que falla en fase 2")
	}
	last := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
	if strings.Contains(last, "Veredicto pre-merge") {
		t.Errorf("la re-edición de fallo no debe traer veredicto:\n%s", last)
	}
	if !strings.Contains(last, "La revisión falló") {
		t.Errorf("la re-edición de fallo debe mantener el estado (§9.7):\n%s", last)
	}
}

// El prompt embebido existe, trae el hueco de idioma y declara su carácter
// advisory — contrato del archivo, no del agente (§9.5).
func TestPremergePromptContrato(t *testing.T) {
	if !strings.Contains(promptPremerge, languagePlaceholder) {
		t.Errorf("el prompt sin hueco de idioma ({{LANGUAGE}})")
	}
	if !strings.Contains(promptPremerge, "nunca bloquea el merge") {
		t.Errorf("el prompt debe declarar que el veredicto es advisory (§1.1)")
	}
	for _, v := range []string{"apto", "apto_con_observaciones", "no_apto"} {
		if !strings.Contains(promptPremerge, `"`+v+`"`) {
			t.Errorf("al prompt le falta el valor del conjunto cerrado %q", v)
		}
	}
	if v := prompts.Version(); len(v) != 64 {
		t.Errorf("prompts.Version() = %d chars, querés sha-256 de 64 (premerge incluido)", len(v))
	}
}
