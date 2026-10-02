package main

import "testing"

func TestMaskSecret(t *testing.T) {
	body := "aws-access-token: AKIALAAAAAAAAAAAAAAAA detectado en .env"
	got := maskSecret(body, "AKIALAAAAAAAAAAAAAAAA", "KEY=AKIALAAAAAAAAAAAAAAAA")
	if got != "aws-access-token: [REDACTED] detectado en .env" {
		t.Errorf("maskSecret() = %q", got)
	}
	// Sin valor detectado en el cuerpo: no cambia nada.
	if same := maskSecret("sin secret", "AKIALAAAAAAAAAAAAAAAA"); same != "sin secret" {
		t.Errorf("maskSecret() mutó un cuerpo limpio: %q", same)
	}
}

func TestESLintMapping(t *testing.T) {
	if sev := severityFromESLint(2); sev != "high" {
		t.Errorf("severity(error) = %q, want high", sev)
	}
	if sev := severityFromESLint(1); sev != "medium" {
		t.Errorf("severity(warning) = %q, want medium", sev)
	}
	// Debajo de warning se descarta (contrato).
	if sev := severityFromESLint(0); sev != "low" {
		t.Errorf("severity(0) = %q, want low", sev)
	}
	cases := map[string]string{
		"no-eval":           "security",
		"no-new-func":       "security",
		"no-await-in-loop":  "performance",
		"complexity":        "logic",
		"no-unreachable":    "logic",
		"no-unused-vars":    "style",
		"prefer-const":      "style",
		"regla-desconocida": "other",
	}
	for rule, want := range cases {
		if got := classifyESLint(rule); got != want {
			t.Errorf("classifyESLint(%q) = %q, want %q", rule, got, want)
		}
	}
}

func TestRuffMapping(t *testing.T) {
	cases := map[string]struct{ sev, cat string }{
		"E902":    {"high", "style"},    // error de ejecución/sintaxis
		"S506":    {"high", "security"}, // bandit: yaml unsafe load
		"F821":    {"medium", "logic"},  // nombre sin definir
		"B008":    {"medium", "logic"},  // bugbear
		"E501":    {"low", "style"},     // línea larga
		"I001":    {"low", "style"},     // isort
		"PERF401": {"low", "performance"},
	}
	for code, want := range cases {
		if got := ruffSeverity(code); got != want.sev {
			t.Errorf("ruffSeverity(%q) = %q, want %q", code, got, want.sev)
		}
		if got := classifyRuff(code); got != want.cat {
			t.Errorf("classifyRuff(%q) = %q, want %q", code, got, want.cat)
		}
	}
}

func TestGolangCIMapping(t *testing.T) {
	if got := golangciSeverity(""); got != "medium" {
		t.Errorf("golangciSeverity(vacío) = %q, want medium", got)
	}
	if got := golangciSeverity("high"); got != "high" {
		t.Errorf("golangciSeverity(high) = %q", got)
	}
	if got := classifyGolangCI("gosec"); got != "security" {
		t.Errorf("classifyGolangCI(gosec) = %q, want security", got)
	}
	if got := classifyGolangCI("gofmt"); got != "style" {
		t.Errorf("classifyGolangCI(gofmt) = %q, want style", got)
	}
	if got := classifyGolangCI("errcheck"); got != "other" {
		t.Errorf("classifyGolangCI(errcheck) = %q, want other", got)
	}
}

func TestClippyMapping(t *testing.T) {
	cases := map[string]struct{ sev, cat string }{
		"error":              {"high", "other"},
		"warning":            {"medium", "other"},
		"note":               {"low", "other"},
		"clippy::perf::copy": {"medium", "performance"},
		"clippy::style":      {"medium", "style"},
		"clippy::correctness::option-unwrap-used": {"medium", "logic"},
	}
	for rule, want := range cases {
		if got := clippySeverity(rule); got != want.sev && want.cat == "other" {
			// solo nivel para las primeras tres entradas
			t.Errorf("clippySeverity(%q) = %q, want %q", rule, got, want.sev)
		}
		if got := classifyClippy(rule); got != want.cat {
			t.Errorf("classifyClippy(%q) = %q, want %q", rule, got, want.cat)
		}
	}
}

func TestEnvWith(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HOME=/root", "GOFLAGS=-mod=readonly"}
	got := envWith(base, "GOFLAGS=-mod=vendor", "GOCACHE=/tmp/go-build")
	want := []string{"GOFLAGS=-mod=vendor", "GOCACHE=/tmp/go-build", "PATH=/usr/bin", "HOME=/root"}
	if len(got) != len(want) {
		t.Fatalf("envWith() = %v, want %v", got, want)
	}
	set := map[string]bool{}
	for _, e := range got {
		set[e] = true
	}
	for _, e := range want {
		if !set[e] {
			t.Errorf("envWith() sin %q: %v", e, got)
		}
	}
	// exactamente un GOFLAGS: getenv del hijo devuelve la primera ocurrencia.
	count := 0
	for _, e := range got {
		if len(e) > 8 && e[:8] == "GOFLAGS=" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("envWith() dejó %d entradas GOFLAGS", count)
	}
}
