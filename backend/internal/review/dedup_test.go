// Tests de anclas de dedup (§6 F4): resolución del símbolo contenedor,
// modo dual de IsDuplicate (símbolo exacto vs drift numérico legacy) y el
// cableado punta a punta en Run — símbolos en comments_sent con extractor,
// anclas numéricas sin él y fallback con log ante fallo de extracción.
package review

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Andres39128/codeowl/backend/internal/analyze"
	"github.com/Andres39128/codeowl/backend/internal/store"
)

// fixtures de símbolos: dos anidados en a.go, uno al borde y otro en b.go.
func anchorSymbols() []analyze.Symbol {
	return []analyze.Symbol{
		{File: "a.go", Symbol: "Outer", Kind: "class", StartLine: 10, EndLine: 50},
		{File: "a.go", Symbol: "Inner", Kind: "function", StartLine: 20, EndLine: 30},
		{File: "a.go", Symbol: "Edge", Kind: "function", StartLine: 60, EndLine: 70},
		{File: "b.go", Symbol: "Other", Kind: "class", StartLine: 1, EndLine: 99},
	}
}

// anchorFor (§6 F4): contenedor por archivo + rango de líneas, el MÁS
// INTERNO cuando hay anidamiento, bordes inclusive; sin contenedor → línea.
func TestAnchorFor(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		line    int32
		want    string
		wantSym bool
	}{
		{"sin contenedor", "a.go", 5, "5", false},
		{"contorno", "a.go", 15, "Outer", true},
		{"más interno en anidamiento", "a.go", 25, "Inner", true},
		{"borde inicial del interno", "a.go", 20, "Inner", true},
		{"borde final del interno", "a.go", 30, "Inner", true},
		{"borde inicial del contenedor", "a.go", 10, "Outer", true},
		{"borde final del contenedor", "a.go", 50, "Outer", true},
		{"otro símbolo del mismo archivo", "a.go", 65, "Edge", true},
		{"archivo distinto nunca matchea", "c.go", 25, "25", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			anchor, isSym := anchorFor(Finding{File: c.file, Line: c.line}, anchorSymbols())
			if anchor != c.want || isSym != c.wantSym {
				t.Errorf("anchorFor(%s:%d) = (%q, %v), querés (%q, %v)",
					c.file, c.line, anchor, isSym, c.want, c.wantSym)
			}
		})
	}
	// Sin símbolos (extractor falló o no implementado): siempre línea.
	if a, sym := anchorFor(Finding{File: "a.go", Line: 25}, nil); a != "25" || sym {
		t.Errorf("sin símbolos anchorFor = (%q, %v), querés (\"25\", false)", a, sym)
	}
}

// IsDuplicate dual (§6 F4): consulta simbólica matchea por igualdad exacta
// contra filas simbólicas y por drift contra filas numéricas legacy
// (transición); consulta numérica o vacía solo drift numérico — las filas
// simbólicas jamás matchean (kinds de ancla distintos).
func TestIsDuplicateDualMode(t *testing.T) {
	row := func(anchor string, valid bool) store.CommentsSent {
		return store.CommentsSent{
			File:     pgtype.Text{String: "a.go", Valid: true},
			Category: pgtype.Text{String: "logic", Valid: true},
			Anchor:   pgtype.Text{String: anchor, Valid: valid},
		}
	}
	cases := []struct {
		name     string
		rows     []store.CommentsSent
		file     string
		category string
		line     int32
		anchor   string
		drift    int
		want     bool
	}{
		{"símbolo igual", []store.CommentsSent{row("Handler", true)}, "a.go", "logic", 42, "Handler", 3, true},
		{"símbolo distinto", []store.CommentsSent{row("Handler", true)}, "a.go", "logic", 42, "Other", 3, false},
		{"símbolo vs legacy línea dentro del drift", []store.CommentsSent{row("10", true)}, "a.go", "logic", 12, "Handler", 3, true},
		{"símbolo vs legacy línea fuera del drift", []store.CommentsSent{row("10", true)}, "a.go", "logic", 20, "Handler", 3, false},
		{"numérico vs numérico dentro del drift", []store.CommentsSent{row("10", true)}, "a.go", "logic", 11, "11", 3, true},
		{"numérico vs numérico fuera del drift", []store.CommentsSent{row("10", true)}, "a.go", "logic", 20, "11", 3, false},
		{"numérico vs símbolo no matchea", []store.CommentsSent{row("Handler", true)}, "a.go", "logic", 42, "42", 3, false},
		{"fila sin ancla no deduplica", []store.CommentsSent{row("", false)}, "a.go", "logic", 10, "10", 3, false},
		{"ancla vacía → modo legacy numérico", []store.CommentsSent{row("10", true)}, "a.go", "logic", 12, "", 3, true},
		{"ancla vacía vs símbolo no matchea", []store.CommentsSent{row("Handler", true)}, "a.go", "logic", 42, "", 3, false},
		{"archivo distinto no matchea", []store.CommentsSent{row("Handler", true)}, "b.go", "logic", 42, "Handler", 3, false},
		{"categoría distinta no matchea", []store.CommentsSent{row("Handler", true)}, "a.go", "style", 42, "Handler", 3, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := Finding{File: c.file, Category: c.category, Line: c.line}
			if got := IsDuplicate(c.rows, f, c.anchor, c.drift); got != c.want {
				t.Errorf("IsDuplicate(%s:%d, ancla=%q, drift=%d) = %v, querés %v",
					c.file, c.line, c.anchor, c.drift, got, c.want)
			}
		})
	}
}

// stubSymbolAnalyzer es el analyzer con la capacidad F4: Run heredado del
// stub base y ExtractSymbols con símbolos fijos o error (symErr, indep. del
// err del Run base).
type stubSymbolAnalyzer struct {
	stubAnalyzer
	syms   []analyze.Symbol
	symErr error
	calls  int
}

func (a *stubSymbolAnalyzer) ExtractSymbols(_ context.Context, _ string) ([]analyze.Symbol, error) {
	a.calls++
	if a.symErr != nil {
		return nil, a.symErr
	}
	return a.syms, nil
}

// runWithAnalyzer corre el pipeline con un analyzer arbitrario sobre el
// happy path de un archivo (el harness deps.az es del tipo concreto).
func runWithAnalyzer(t *testing.T, d *deps, az Analyzer) (*ReviewResult, error) {
	t.Helper()
	return Run(context.Background(), d.cfg, d.st, d.gw, az, d.vcs, d.retr, testInput())
}

// symbolAnalyzer arma el stub con capacidad F4 y un resultado SAST válido.
func symbolAnalyzer(syms []analyze.Symbol, findings ...analyze.Finding) *stubSymbolAnalyzer {
	return &stubSymbolAnalyzer{
		stubAnalyzer: stubAnalyzer{result: &analyze.AnalysisResult{FilesAnalyzed: 1, Findings: findings}},
		syms:         syms,
	}
}

// inlineAnchors indexa el ancla de comments_sent inline por categoría.
func inlineAnchors(d *deps) map[string]string {
	out := map[string]string{}
	for _, c := range d.st.comments["inline"] {
		out[c.Category.String] = c.Anchor.String
	}
	return out
}

// Con ExtractSymbols: el hallazgo dentro del span del símbolo se ancla con
// el NOMBRE del símbolo en comments_sent; el de afuera, con la línea.
func TestRunSymbolAnchors(t *testing.T) {
	d := newDeps()
	sa := symbolAnalyzer([]analyze.Symbol{
		{File: "main.go", Symbol: "main", Kind: "function", StartLine: 5, EndLine: 7},
	}, analyze.Finding{File: "main.go", Line: 6, Severity: "low", Category: "style", Body: "estilo", Source: "sast"})
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "inyección", "")} // línea 3: fuera de main
	d.gw.summarizer = []string{summarizerJSON}

	res, err := runWithAnalyzer(t, d, sa)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != StatusSuccess || res.FindingsCount != 2 {
		t.Fatalf("status/count = %s/%d, querés success/2", res.Status, res.FindingsCount)
	}
	if sa.calls != 1 {
		t.Errorf("ExtractSymbols llamado %d veces, querés 1", sa.calls)
	}
	anchors := inlineAnchors(d)
	if anchors["security"] != "3" {
		t.Errorf("ancla security = %q, querés \"3\" (sin símbolo contenedor)", anchors["security"])
	}
	if anchors["style"] != "main" {
		t.Errorf("ancla style = %q, querés \"main\" (símbolo contenedor)", anchors["style"])
	}
}

// Sin ExtractSymbols (stub base, §6 F4 pre-AST): anclas numéricas, el
// comportamiento de siempre — regresión de todos los caminos existentes.
func TestRunLineAnchorsWithoutExtractor(t *testing.T) {
	d := newDeps()
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "inyección", "")}
	d.gw.summarizer = []string{summarizerJSON}
	d.az.result.Findings = []analyze.Finding{
		{File: "main.go", Line: 5, Severity: "low", Category: "style", Body: "estilo", Source: "sast"},
	}

	res, err := d.run(t)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FindingsCount != 2 {
		t.Fatalf("FindingsCount = %d, querés 2", res.FindingsCount)
	}
	anchors := inlineAnchors(d)
	if anchors["security"] != "3" || anchors["style"] != "5" {
		t.Errorf("anclas = %v, querés numéricas {security:3, style:5}", anchors)
	}
}

// Fallo de extracción (§6 F4): fallback a anclas por línea, la revisión
// sigue y publica, y el slog estructurado dispara.
func TestRunSymbolExtractionFails(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	d := newDeps()
	sa := symbolAnalyzer(nil) // SAST válido sin hallazgos; la extracción falla
	sa.symErr = errors.New("podman murió")
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "inyección", "")}
	d.gw.summarizer = []string{summarizerJSON}

	res, err := runWithAnalyzer(t, d, sa)
	if err != nil {
		t.Fatalf("Run: %v (la extracción jamás frena la revisión)", err)
	}
	if res.Status != StatusSuccess || res.FindingsCount != 1 {
		t.Fatalf("status/count = %s/%d, querés success/1", res.Status, res.FindingsCount)
	}
	if anchors := inlineAnchors(d); anchors["security"] != "3" {
		t.Errorf("ancla security = %q, querés \"3\" (fallback a línea)", anchors["security"])
	}
	if !strings.Contains(buf.String(), "extracción de símbolos") {
		t.Errorf("el log de fallback no disparó:\n%s", buf.String())
	}
}

// Dedup de corrida previa con ancla simbólica (§6 F4): el hallazgo dentro
// del mismo símbolo no se republica; el de otro símbolo sí.
func TestRunSymbolDedup(t *testing.T) {
	d := newDeps()
	sa := symbolAnalyzer([]analyze.Symbol{
		{File: "main.go", Symbol: "main", Kind: "function", StartLine: 5, EndLine: 7},
	}, analyze.Finding{File: "main.go", Line: 6, Severity: "low", Category: "style", Body: "estilo", Source: "sast"})
	d.st.comments["inline"] = []store.CommentsSent{{
		ID:            99,
		PullRequestID: testPRID,
		CommentID:     "viejo-1",
		Type:          "inline",
		File:          pgtype.Text{String: "main.go", Valid: true},
		Category:      pgtype.Text{String: "style", Valid: true},
		Anchor:        pgtype.Text{String: "main", Valid: true},
	}}
	d.gw.reviewer["main.go"] = []string{reviewerFindings(3, "high", "security", "inyección", "")}
	d.gw.summarizer = []string{summarizerJSON}

	res, err := runWithAnalyzer(t, d, sa)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FindingsCount != 1 {
		t.Errorf("FindingsCount = %d, querés 1 (el del símbolo main deduplica)", res.FindingsCount)
	}
	if n := len(d.vcs.inlineReqs) + len(d.vcs.suggestReqs); n != 1 {
		t.Errorf("inline publicados = %d, querés 1", n)
	}
}
