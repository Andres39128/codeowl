package analyze

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Image: "localhost/codeowl-analyzer:latest", Memory: "1g",
		PidsLimit: 256, Timeout: 2 * time.Minute, TmpfsSize: "512m",
	}
}

// fixture de salida del analyzer: linter ok, linter skipped, secret enmascarado.
const fixtureJSON = `{
  "files_analyzed": 2,
  "linters_run": [
    {"name": "eslint", "status": "ok", "findings_count": 1},
    {"name": "golangci-lint", "status": "skipped", "reason": "no dependencies available"},
    {"name": "gitleaks", "status": "ok", "findings_count": 1}
  ],
  "findings": [
    {"file": "src/app.js", "line": 42, "severity": "high", "category": "style",
     "body": "'x' is defined but never used.", "source": "sast",
     "linter": "eslint", "rule": "no-unused-vars"},
    {"file": ".env.example", "line": 3, "severity": "high", "category": "security",
     "body": "[REDACTED]", "source": "sast", "linter": "gitleaks", "rule": "aws-access-token"}
  ]
}`

func TestParseValidOutput(t *testing.T) {
	res, err := Parse([]byte(fixtureJSON))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if res.FilesAnalyzed != 2 {
		t.Errorf("FilesAnalyzed = %d, want 2", res.FilesAnalyzed)
	}
	if len(res.LintersRun) != 3 {
		t.Fatalf("LintersRun = %d entradas, want 3", len(res.LintersRun))
	}
	if res.LintersRun[1].Status != "skipped" || res.LintersRun[1].Reason == "" {
		t.Errorf("golangci-lint debe quedar skipped con razón, got %+v", res.LintersRun[1])
	}
	if len(res.Findings) != 2 {
		t.Fatalf("Findings = %d, want 2", len(res.Findings))
	}
	// §9.4: el valor del secret jamás viaja — solo ruta, línea y regla.
	sec := res.Findings[1]
	if sec.Linter != "gitleaks" || sec.Body != "[REDACTED]" {
		t.Errorf("hallazgo de gitleaks mal normalizado: %+v", sec)
	}
	if res.Findings[0].Severity != "high" {
		t.Errorf("severity del finding eslint = %q, want high", res.Findings[0].Severity)
	}
}

func TestParseRejectsClosedSetViolations(t *testing.T) {
	cases := []struct {
		name string
		json string
	}{
		{"severity inválida", `{"files_analyzed":1,"linters_run":[{"name":"ruff","status":"ok"}],
			"findings":[{"file":"a.py","line":1,"severity":"critical","category":"style","body":"x","source":"sast","linter":"ruff","rule":"E501"}]}`},
		{"category inválida", `{"files_analyzed":1,"linters_run":[{"name":"ruff","status":"ok"}],
			"findings":[{"file":"a.py","line":1,"severity":"high","category":"smell","body":"x","source":"sast","linter":"ruff","rule":"E501"}]}`},
		{"source distinto de sast", `{"files_analyzed":1,"linters_run":[{"name":"ruff","status":"ok"}],
			"findings":[{"file":"a.py","line":1,"severity":"high","category":"style","body":"x","source":"llm","linter":"ruff","rule":"E501"}]}`},
		{"sin linters_run", `{"files_analyzed":1,"findings":[]}`},
		{"no es JSON", `algo roto`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.json)); err == nil {
				t.Fatalf("Parse() esperaba error, no hubo")
			}
		})
	}
}

func TestParseEmptyFindingsIsCleanRun(t *testing.T) {
	res, err := Parse([]byte(`{"files_analyzed":0,"linters_run":[{"name":"gitleaks","status":"ok"}],"findings":[]}`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %d, want 0", len(res.Findings))
	}
}

// TestBuildRunArgs verifica los flags de seguridad de §9.4 sin ejecutar podman.
func TestBuildRunArgs(t *testing.T) {
	args := buildRunArgs("/tmp/job-123", testConfig())
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--network=none", "--read-only",
		"--pids-limit 256", "--memory 1g",
		"--tmpfs /tmp:size=512m",
		"/tmp/job-123:/workdir:ro",
		"localhost/codeowl-analyzer:latest",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("buildRunArgs() sin %q: %v", want, args)
		}
	}
	if args[len(args)-2] != "localhost/codeowl-analyzer:latest" || args[len(args)-1] != "/workdir" {
		t.Errorf("imagen y workdir deben ser los últimos argumentos: %v", args)
	}
}

// --- Pruebas con imagen (mapa: "imagen analyzer en CI") -----------------------
//
// Requieren la imagen construida: just analyzer-build. Si no existe, se
// saltan — las unit de arriba no la necesitan.

func imageAvailable(t *testing.T) {
	t.Helper()
	if err := exec.Command("podman", "image", "exists", testConfig().Image).Run(); err != nil {
		t.Skipf("imagen %s no disponible (just analyzer-build)", testConfig().Image)
	}
}

// fixtureRepo crea un repo con: error de lint JS (no-unused-vars), secret AWS
// de prueba (falso pero con la forma y entropía que el ruleset default de
// Gitleaks exige) y un go.mod sin vendor (golangci-lint debe quedar skipped).
func fixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/app.js", "const unused = 1;\nconsole.log('hi');\n")
	write(".env", "AWS_ACCESS_KEY_ID=AKIAQ7GTLKDZ3RNWPX2M\n")
	write("go.mod", "module ejemplo\n\ngo 1.27\n")
	return dir
}

func TestRunAgainstFixture(t *testing.T) {
	imageAvailable(t)
	runner := NewRunner(testConfig())
	res, err := runner.Run(context.Background(), fixtureRepo(t))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	byName := map[string]LinterStatus{}
	for _, l := range res.LintersRun {
		byName[l.Name] = l
	}
	if byName["eslint"].Status != "ok" || byName["eslint"].FindingsCount == 0 {
		t.Errorf("eslint debió correr con hallazgos, got %+v", byName["eslint"])
	}
	// go.mod sin go.sum/vendor → skipped, nunca error (§9.4 best-effort).
	if byName["golangci-lint"].Status != "skipped" {
		t.Errorf("golangci-lint debió quedar skipped, got %+v", byName["golangci-lint"])
	}
	if byName["gitleaks"].Status != "ok" || byName["gitleaks"].FindingsCount == 0 {
		t.Errorf("gitleaks debió detectar el secret de prueba, got %+v", byName["gitleaks"])
	}

	var lintFinding, secretFinding *Finding
	for i := range res.Findings {
		switch {
		case res.Findings[i].Linter == "eslint" && res.Findings[i].Rule == "no-unused-vars":
			lintFinding = &res.Findings[i]
		case res.Findings[i].Linter == "gitleaks":
			secretFinding = &res.Findings[i]
		}
	}
	if lintFinding == nil || lintFinding.Rule != "no-unused-vars" {
		t.Fatalf("falta el hallazgo no-unused-vars de eslint: %+v", res.Findings)
	}
	// error de ESLint (severity 2) → high (mapeo del contrato).
	if lintFinding.Severity != "high" {
		t.Errorf("severity = %q, want high", lintFinding.Severity)
	}
	if secretFinding == nil {
		t.Fatalf("falta el hallazgo de gitleaks: %+v", res.Findings)
	}
	// El valor del secret (falso) jamás aparece en la salida completa.
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), "AKIAQ7GTLKDZ3RNWPX2M") {
		t.Errorf("la salida contiene el valor del secret — viola §9.4: %s", raw)
	}
	if secretFinding.Category != "security" {
		t.Errorf("categoría de gitleaks = %q, want security", secretFinding.Category)
	}
}

// --- smoke del runner contra podman ausente -----------------------------------

func TestRunFailsWithoutPodman(t *testing.T) {
	if _, err := exec.LookPath("podman"); err == nil {
		t.Skip("podman existe; solo aplica en entornos sin podman")
	}
	runner := NewRunner(testConfig())
	if _, err := runner.Run(context.Background(), t.TempDir()); err == nil {
		t.Fatal("Run() esperaba error sin podman")
	}
}
