package review

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestCleanMermaid cubre el validador estructural de Mermaid (§9.8): el
// sequenceDiagram se valida línea a línea; los demás tipos quedan en
// encabezado (el render lo hace el VCS, el guard corta basura evidente).
func TestCleanMermaid(t *testing.T) {
	// alt x90 anidado: la pila es explícita, el anidamiento profundo válido.
	var deep strings.Builder
	deep.WriteString("sequenceDiagram\n")
	for i := 0; i < 90; i++ {
		deep.WriteString(strings.Repeat(" ", i+1) + "alt caso\n")
	}
	deep.WriteString("A->>B: dentro\n")
	for i := 90; i > 0; i-- {
		deep.WriteString(strings.Repeat(" ", i) + "end\n")
	}

	var tooManyLines strings.Builder
	tooManyLines.WriteString("sequenceDiagram\n")
	for i := 0; i < mermaidMaxLines+1; i++ {
		tooManyLines.WriteString("A->>B: x\n")
	}

	cases := []struct {
		name string
		in   string
		want bool
	}{
		// --- sequenceDiagram válidos ---
		{"dos mensajes mínimos", "sequenceDiagram\nA->>B: hola\nB-->>A: chau", true},
		{"participantes implícitos por primer uso", "sequenceDiagram\nAlice->>Bob: ping\nBob->>Charlie: relay", true},
		{"participant y actor con as label", "sequenceDiagram\nparticipant API as Backend API\nactor U as Usuario\nU->>API: GET /x", true},
		{"activate y deactivate", "sequenceDiagram\nparticipant A\nA->>A: solo\nactivate A\ndeactivate A", true},
		{"note left, right y over", "sequenceDiagram\nA->>B: x\nnote left of A: ojo\nnote right of B: listo\nnote over A: centro\nnote over A,B: ambos", true},
		{"alt con else", "sequenceDiagram\nA->>B: x\nalt ok\nB-->>A: bien\nelse error\nB-->>A: mal\nend", true},
		{"loop", "sequenceDiagram\nloop cada minuto\nA->>B: tick\nend", true},
		{"par con and", "sequenceDiagram\npar rama1\nA->>B: 1\nand rama2\nA->>C: 2\nend", true},
		{"critical con option", "sequenceDiagram\ncritical pago\nA->>B: cobrar\noption reintento\nA->>B: reintentar\nend", true},
		{"break", "sequenceDiagram\nbreak corta\nA->>B: fin\nend", true},
		{"autonumber simple y con N", "sequenceDiagram\nautonumber\nA->>B: 1\nautonumber 5\nB-->>A: 2", true},
		{"title", "sequenceDiagram\ntitle Flujo de compra\nA->>B: x", true},
		{"comentarios %% y líneas en blanco", "sequenceDiagram\n%% encabezado\n\nA->>B: x\n\n%% pie", true},
		{"cerco ```mermaid de transporte", "```mermaid\nsequenceDiagram\nA->>B: x\n```", true},
		{"texto de mensaje vacío tras dos puntos", "sequenceDiagram\nA->>B:", true},
		{"todas las flechas", "sequenceDiagram\nA->>B: a\nA-->>B: b\nA->B: c\nA-->B: d\nA-x B: e\nA--x B: f\nA-) B: g\nA--) B: h", true},
		{"%% a media línea es texto, no comentario", "sequenceDiagram\nA->>B: 50%% off", true},
		{"anidamiento profundo dentro del límite", deep.String(), true},

		// --- sequenceDiagram inválidos ---
		{"alt sin end", "sequenceDiagram\nA->>B: x\nalt caso\nB-->>A: y", false},
		{"end sin bloque abierto", "sequenceDiagram\nA->>B: x\nend", false},
		{"else fuera de alt", "sequenceDiagram\nA->>B: x\nelse rara", false},
		{"and fuera de par", "sequenceDiagram\nA->>B: x\nand rara", false},
		{"option fuera de critical", "sequenceDiagram\nA->>B: x\noption rara", false},
		{"deactivate de desconocido", "sequenceDiagram\nA->>B: x\ndeactivate Z", false},
		{"activate de desconocido", "sequenceDiagram\nA->>B: x\nactivate Z", false},
		{"línea basura", "sequenceDiagram\nA->>B: x\nholamundo", false},
		{"mensaje sin dos puntos", "sequenceDiagram\nA->>B hola", false},
		{"mensaje con flecha desconocida", "sequenceDiagram\nA->*B: x", false},
		{"solo el encabezado, sin contenido", "sequenceDiagram", false},
		{"sequenceDiagram con sufijo basura", "sequenceDiagram de verdad", false},
		{"autonumber no numérico", "sequenceDiagram\nautonumber mucho\nA->>B: x", false},
		{"nota sobre desconocido", "sequenceDiagram\nA->>B: x\nnote over Z: ojo", false},
		{"nota sin dos puntos", "sequenceDiagram\nA->>B: x\nnote over A ojo", false},
		{"cerco sin cerrar", "```mermaid\nsequenceDiagram\nA->>B: x", false},

		// --- límites defensivos (§9.8) ---
		{"más de mermaidMaxLines", tooManyLines.String(), false},
		{"línea más larga que mermaidMaxLineLen", "sequenceDiagram\nA->>B: " + strings.Repeat("x", mermaidMaxLineLen), false},

		// --- otros tipos: encabezado alcanza (tradeoff documentado) ---
		{"graph TD", "graph TD\n A-->B", true},
		{"graph solo sin cuerpo pasa (el guard no es renderer)", "graph", true},
		{"flowchart LR", "flowchart LR\n A-->B", true},
		{"stateDiagram-v2", "stateDiagram-v2\n[*] --> s1", true},
		{"classDiagram", "classDiagram\nclass Foo", true},
		{"erDiagram", "erDiagram\nUSUARIO ||--o{ PEDIDO : hace", true},
		{"gantt", "gantt\nsection A\ntarea :a1, 2026-01-01, 3d", true},
		{"pie", "pie\nkey 1 : 50", true},
		{"mindmap", "mindmap\nraiz\n  hoja", true},
		{"timeline", "timeline\ntitle Historia\n2026 : hito", true},

		// --- basura general ---
		{"cadena vacía", "", false},
		{"texto sin encabezado", "hola mundo", false},
		{"encabezado desconocido", "piechart\nx: 1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := validMermaid(c.in); got != c.want {
				t.Errorf("validMermaid(%q) = %v, querés %v", c.in, got, c.want)
			}
		})
	}
}

// TestCleanMermaidStripFence: el valor devuelto pierde el cerco de transporte
// — publication.go lo reenvuelve y un cerco interno rompería el render (§9.5).
func TestCleanMermaidStripFence(t *testing.T) {
	got, err := cleanMermaid("```mermaid\nsequenceDiagram\nA->>B: x\n```")
	if err != nil {
		t.Fatalf("cerco válido no debería fallar: %v", err)
	}
	if got != "sequenceDiagram\nA->>B: x" {
		t.Errorf("cleanMermaid = %q, querés el diagrama sin cerco", got)
	}
}

// TestRunSummarizerMermaidOmit: el resumen nunca falla por el diagrama —
// mermaid inválido se omite con registro (§9.8) y el válido se publica sin
// cerco de transporte.
func TestRunSummarizerMermaidOmit(t *testing.T) {
	cases := []struct {
		name        string
		mermaid     string
		wantMermaid string
	}{
		{"inválido se omite", "sequenceDiagram\nholamundo", ""},
		{"válido con cerco se limpia", "```mermaid\nsequenceDiagram\nA->>B: x\n```", "sequenceDiagram\nA->>B: x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, merr := json.Marshal(c.mermaid)
			if merr != nil {
				t.Fatalf("marshal mermaid: %v", merr)
			}
			gw := newStubGateway()
			gw.summarizer = []string{`{"summary": "resumen", "mermaid": ` + string(m) + `}`}
			s, err := runSummarizer(context.Background(), gw, Config{}, "es", "stats", nil)
			if err != nil {
				t.Fatalf("runSummarizer: %v", err)
			}
			if s.Summary != "resumen" {
				t.Errorf("summary = %q, querés que sobreviva al mermaid", s.Summary)
			}
			if s.Mermaid != c.wantMermaid {
				t.Errorf("mermaid = %q, querés %q", s.Mermaid, c.wantMermaid)
			}
		})
	}
}
