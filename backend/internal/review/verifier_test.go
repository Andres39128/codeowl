// Tests del Verifier (F3, rol cheap — §3.3/§6 F3/§9.6): marcaje verified,
// falsos positivos confirmados no publicados pero persistidos, degradación
// sin proveedor cheap (§9.6), descarte por salida malformada (§9.8) y SAST
// jamás verificado. Reusa stubStore/stubGateway de review_test.go.
package review

import (
	"strings"
	"testing"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// verifiedOf busca el hallazgo persistido por cuerpo y devuelve su estado
// verified (pgtype.Bool crudo para afirmar null vs true/false).
func verifiedOf(t *testing.T, d *deps, body string) (file string, verifiedOK, verifiedVal bool) {
	t.Helper()
	for _, f := range d.st.findings {
		if strings.Contains(f.Body, body) {
			return f.File, f.Verified.Valid, f.Verified.Bool
		}
	}
	t.Fatalf("no se persistió ningún finding con cuerpo %q", body)
	return "", false, false
}

// Verifier corre (rol cheap configurado): marca true el corroborado, false
// el falso positivo confirmado — que no se publica pero SÍ persiste — y
// deja null al SAST (determinístico, §3.3). Sin declaración de omisión.
func TestRunVerifierMarksFindings(t *testing.T) {
	d := newDeps()
	d.st.cheapProviders = []store.LlmProvider{{ID: 1, Role: roleCheap, Enabled: true}}
	d.gw.reviewer["main.go"] = []string{
		"[" + strings.Trim(reviewerFindings(3, "high", "security", "inyección SQL", "db.Query(q, arg)"), "[]") + "," +
			strings.Trim(reviewerFindings(5, "medium", "logic", "chequeo faltante", ""), "[]") + "]",
	}
	d.gw.verifier = []string{`[
		{"index": 0, "verified": true, "reason": "el hunk muestra la interpolación"},
		{"index": 1, "verified": false, "reason": "la línea ya valida la entrada"}
	]`}
	d.gw.summarizer = []string{summarizerJSON}
	d.az.result.Findings = []analyze.Finding{{
		File: "main.go", Line: 5, Severity: "low", Category: "style",
		Body: "nombre corto", Source: "sast", Linter: "revive", Rule: "var-naming",
	}}

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, querés success (el verifier jamás marca partial, §9.6)", res.Status)
	}
	// Publicados: el LLM verificado true + el SAST; el falso positivo
	// confirmado NO se publica (§6 F3).
	if res.FindingsCount != 2 {
		t.Errorf("FindingsCount = %d, querés 2 (el verified=false queda fuera)", res.FindingsCount)
	}
	if len(d.vcs.suggestReqs) != 1 || d.vcs.suggestReqs[0].Line != 3 {
		t.Errorf("publicados = %+v, querés solo el LLM verificado en la línea 3", d.vcs.suggestReqs)
	}
	if len(d.vcs.inlineReqs) != 1 || d.vcs.inlineReqs[0].Line != 5 {
		t.Errorf("inline = %+v, querés solo el SAST en la línea 5", d.vcs.inlineReqs)
	}
	// Persistencia: true, false y null — auditable en findings (§3.3).
	if len(d.st.findings) != 3 {
		t.Fatalf("findings persistidos = %d, querés 3 (los no publicados también)", len(d.st.findings))
	}
	if _, ok, val := verifiedOf(t, d, "inyección SQL"); !ok || !val {
		t.Errorf("el LLM corroborado debe persistir verified=true (valid=%v, val=%v)", ok, val)
	}
	if _, ok, val := verifiedOf(t, d, "chequeo faltante"); !ok || val {
		t.Errorf("el falso positivo confirmado debe persistir verified=false (valid=%v, val=%v)", ok, val)
	}
	if _, ok, _ := verifiedOf(t, d, "nombre corto"); ok {
		t.Error("el SAST es determinístico: debe persistir con verified null (§3.3)")
	}
	// El prompt del verifier trajo el lote indexado, la referencia SAST y
	// los hunks — un solo llamado por archivo.
	if len(d.gw.verifierSeen) != 1 {
		t.Fatalf("llamadas al verifier = %d, querés 1", len(d.gw.verifierSeen))
	}
	prompt := d.gw.verifierSeen[0]
	for _, want := range []string{verifierUserMarker, `"index": 0`, `"index": 1`, "inyección SQL", "nombre corto", "```diff"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("el prompt del verifier debe traer %q:\n%s", want, prompt)
		}
	}
	// Corrida verificada: sin declaración de omisión.
	final := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
	if strings.Contains(final, "Verificación de hallazgos omitida") {
		t.Errorf("la corrida verificada no debe declarar omisión:\n%s", final)
	}
}

// Sin proveedor del rol cheap (§9.6): el verifier no corre, los findings LLM
// se publican con verified null y la re-edición de fase 2 lo declara. La
// omisión jamás marca partial.
func TestRunVerifierNoCheap(t *testing.T) {
	d := newDeps() // cheapProviders vacío
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
	d.gw.summarizer = []string{summarizerJSON}

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, querés success (la omisión no es cobertura parcial, §9.6)", res.Status)
	}
	if res.FindingsCount != 1 || len(d.vcs.inlineReqs) != 1 {
		t.Fatalf("el hallazgo debe publicarse igual: count=%d, inline=%+v", res.FindingsCount, d.vcs.inlineReqs)
	}
	if _, ok, _ := verifiedOf(t, d, "x"); ok {
		t.Error("sin verifier el finding debe persistir con verified null (§3.3)")
	}
	if d.gw.calls != 2 {
		t.Errorf("gateway llamado %d veces, querés 2 (reviewer + summarizer; cero cheap)", d.gw.calls)
	}
	final := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
	if !strings.Contains(final, "Verificación de hallazgos omitida (sin proveedor del rol cheap)") {
		t.Errorf("la re-edición de fase 2 debe declarar la omisión (§9.6):\n%s", final)
	}
}

// Salida malformada del verifier (§9.8): reintentos por archivo; agotados,
// los hallazgos del lote quedan verified null y SE publican (honesto: el
// verifier no pudo verificar). Con un solo archivo, el fallo total se
// declara — y la corrida sigue success.
func TestRunVerifierMalformed(t *testing.T) {
	d := newDeps()
	d.cfg.AgentRetries = 1
	d.st.cheapProviders = []store.LlmProvider{{ID: 1, Role: roleCheap, Enabled: true}}
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "x", "")}
	d.gw.verifier = []string{"no es json", `{"index": 0}`} // malformado en ambos intentos
	d.gw.summarizer = []string{summarizerJSON}

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusSuccess {
		t.Errorf("status = %q, querés success (el descarte no aborta, §9.8)", res.Status)
	}
	if len(d.gw.verifierSeen) != 2 {
		t.Errorf("intentos del verifier = %d, querés 2 (1 salida + 1 reintento)", len(d.gw.verifierSeen))
	}
	if res.FindingsCount != 1 || len(d.vcs.inlineReqs) != 1 {
		t.Errorf("el hallazgo no verificado debe publicarse: count=%d, inline=%d", res.FindingsCount, len(d.vcs.inlineReqs))
	}
	if _, ok, _ := verifiedOf(t, d, "x"); ok {
		t.Error("el lote descartado debe persistir con verified null")
	}
	final := d.vcs.summaryBods[len(d.vcs.summaryBods)-1]
	if !strings.Contains(final, "Verificación de hallazgos omitida") {
		t.Errorf("con todos los lotes descartados la re-edición debe declararlo:\n%s", final)
	}
}

// parseVerdicts (§9.8): estricto — exactamente un veredicto por hallazgo,
// índices en rango, sin duplicados, con reason.
func TestParseVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		content string
		n       int
		want    map[int]bool
		wantErr bool
	}{
		{"válido", `[{"index":0,"verified":true,"reason":"ok"},{"index":1,"verified":false,"reason":"ya valida"}]`, 2,
			map[int]bool{0: true, 1: false}, false},
		{"con fences", "```json\n[{\"index\":0,\"verified\":true,\"reason\":\"ok\"}]\n```", 1,
			map[int]bool{0: true}, false},
		{"índice fuera de rango", `[{"index":1,"verified":true,"reason":"ok"}]`, 1, nil, true},
		{"índice negativo", `[{"index":-1,"verified":true,"reason":"ok"}]`, 1, nil, true},
		{"índice duplicado", `[{"index":0,"verified":true,"reason":"ok"},{"index":0,"verified":false,"reason":"otro"}]`, 2, nil, true},
		{"faltan veredictos", `[{"index":0,"verified":true,"reason":"ok"}]`, 2, nil, true},
		{"reason vacío", `[{"index":0,"verified":true,"reason":"  "}]`, 1, nil, true},
		{"no es JSON", "perdón, no puedo", 1, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseVerdicts(c.content, c.n)
			if c.wantErr {
				if err == nil {
					t.Fatalf("parseVerdicts(%q, %d) debería fallar", c.content, c.n)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseVerdicts: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("veredictos = %v, querés %v", got, c.want)
			}
			for i, v := range c.want {
				if g, ok := got[i]; !ok || g != v {
					t.Errorf("veredicto[%d] = %v,%v; querés %v", i, g, ok, v)
				}
			}
		})
	}
}
