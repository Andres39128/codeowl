// Tests del risk scoring (§6 F5, T3): ComputeRisk puro por tabla, la
// heurística IsTestFile y el conteo de sensibles con los patrones default.
package review

import "testing"

func TestComputeRisk(t *testing.T) {
	tests := []struct {
		name string
		in   RiskInput
		want int
	}{
		{"diff vacío (zero-value)", RiskInput{}, 0},
		{"diff chico", RiskInput{ChangedLines: 30}, 5},
		{"banda media", RiskInput{ChangedLines: 300}, 25},
		{"banda grande", RiskInput{ChangedLines: 1000}, 30},
		{"diff enorme sin más", RiskInput{ChangedLines: 5000}, 35},
		{"sensibles saturan el tope", RiskInput{ChangedLines: 30, SensitiveHits: 9}, 30}, // 5 + min(9,5)*5
		{"hallazgos saturan el tope", RiskInput{Counts: map[string]int{"high": 3, "medium": 4, "low": 10}}, 30},
		{"hallazgos mixtos", RiskInput{ChangedLines: 30, Counts: map[string]int{"high": 1, "medium": 1, "low": 1}}, 24}, // 5+12+5+2
		{"diff grande sin tests", RiskInput{ChangedLines: 400, Files: []string{"a.go", "b.go"}}, 35},                    // 25 + 10
		{"diff mayormente tests no suma por tests", RiskInput{ChangedLines: 400, Files: []string{"a_test.go", "b_test.go"}, TestFiles: 2}, 25},
		{"diff chico no se castiga por tests", RiskInput{ChangedLines: 20, Files: []string{"a.go"}}, 5},
		{"mixto completo", RiskInput{ChangedLines: 700, Files: []string{"a.go", "b.go"}, SensitiveHits: 1, Counts: map[string]int{"high": 1}}, 57}, // 30+5+12+10
		{"clamp en 100", RiskInput{ChangedLines: 5000, Files: []string{"a.go"}, SensitiveHits: 5, Counts: map[string]int{"high": 9}}, 100},
		{"negativos clampan a 0", RiskInput{ChangedLines: -5, SensitiveHits: -3}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeRisk(tt.in); got != tt.want {
				t.Errorf("ComputeRisk(%+v) = %d, querés %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsTestFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"main_test.go", true},
		{"internal/server/handler_test.go", true},
		{"pkg/x/usuario_test.py", true},
		{"app/usuario_test.js", true},
		{"ui/boton.test.ts", true},
		{"ui/boton.test.tsx", true},
		{"ui/boton.spec.ts", true},
		{"ui/boton.spec.tsx", true},
		{"tests/helpers.go", true},
		{"test/fixtures/datos.json", true},
		{"main.go", false},
		{"test_usuario.go", false},      // prefijo, no sufijo _test
		{"cmd/testutil/load.go", false}, // segmento testutil ≠ test
		{"src/main_test.go.bak", false}, // sufijo real es .bak
		{"", false},
	}
	for _, tt := range tests {
		if got := IsTestFile(tt.path); got != tt.want {
			t.Errorf("IsTestFile(%q) = %v, querés %v", tt.path, got, tt.want)
		}
	}
}

func TestSensitiveHits(t *testing.T) {
	// Con los patrones default: auth, workflows, deploy, migrations y
	// secretos raíz matchean; código común no.
	if got := SensitiveHits([]string{
		"auth/login.go", ".github/workflows/ci.yml", "deploy/k8s.yaml",
		"db/migrations/0001_init.sql", "secrets.yaml",
		"README.md", "main.go", "internal/api/router.go",
	}, defaultRiskSensitivePaths); got != 5 {
		t.Errorf("SensitiveHits(mixtos, defaults) = %d, querés 5", got)
	}
	// Caso mínimo del enunciado: auth/x.go matchea, README no.
	if got := SensitiveHits([]string{"auth/x.go", "README.md"}, defaultRiskSensitivePaths); got != 1 {
		t.Errorf("SensitiveHits({auth/x.go, README.md}) = %d, querés 1", got)
	}
	// Sin patrones no hay bonus (aunque el archivo parezca sensible).
	if got := SensitiveHits([]string{"auth/x.go", "secrets.yaml"}, nil); got != 0 {
		t.Errorf("SensitiveHits sin patrones = %d, querés 0", got)
	}
}
