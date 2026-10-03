// Modo --symbols del analyzer (§6 F4): segunda invocación de la imagen
// sandbox que extrae símbolos e imports vía tree-sitter. El paquete index
// (T4) consume las filas; Retrieve (T5) debe embeber las queries con el
// mismo formato que Index (ver index.SymbolQueryText).
package analyze

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Symbol es una fila del contrato --symbols (mapa analyzer.cli). imports es
// a nivel de archivo y se repite en cada símbolo del mismo (salida plana).
type Symbol struct {
	File      string   `json:"file"`
	Symbol    string   `json:"symbol"`
	Kind      string   `json:"kind"` // function|method|class|type|interface|trait (§3.3: cerrado)
	StartLine int32    `json:"start_line"`
	EndLine   int32    `json:"end_line"`
	Imports   []string `json:"imports"`
}

// validKinds es el conjunto cerrado de kinds de §3.3: la salida del sandbox
// no es confianza — se valida antes de devolverla (mismo criterio que Parse).
var validKinds = map[string]bool{
	"function": true, "method": true, "class": true,
	"type": true, "interface": true, "trait": true,
}

// IsValidKind reporta si kind pertenece al conjunto cerrado de §3.3. El
// consumidor (index) lo usa para filtrar filas de extractores arbitrarios.
func IsValidKind(kind string) bool { return validKinds[kind] }

// symbolReport es el JSON del modo --symbols. Files se mantiene por
// compatibilidad con la salida del recorrido; Symbols es el contrato nuevo.
// Un archivo roto sale con cero filas — ausencia de filas, jamás error (§6 F4:
// un archivo malo no frena la indexación).
type symbolReport struct {
	FilesAnalyzed int          `json:"files_analyzed"`
	Files         []symbolFile `json:"files"`
	Symbols       []Symbol     `json:"symbols"`
}

// symbolFile es la entrada del recorrido: ruta relativa + lenguaje.
type symbolFile struct {
	Path     string `json:"path"`
	Language string `json:"language"`
}

// ParseSymbols valida y tipa la salida JSON del modo --symbols. Falla ante
// JSON inválido o una fila que viole el contrato (kind fuera del conjunto
// cerrado, file/symbol vacíos, líneas inválidas); un archivo roto (en files
// sin filas en symbols) NO es error — es el contrato (§6 F4).
func ParseSymbols(data []byte) ([]Symbol, error) {
	var rep symbolReport
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("salida del analyzer no es JSON válido: %w", err)
	}
	for i, s := range rep.Symbols {
		if !validKinds[s.Kind] {
			return nil, fmt.Errorf("símbolo %d (%s/%s): kind %q fuera del conjunto cerrado", i, s.File, s.Symbol, s.Kind)
		}
		if s.File == "" || s.Symbol == "" {
			return nil, fmt.Errorf("símbolo %d: file y symbol son obligatorios", i)
		}
		if s.StartLine < 1 || s.EndLine < s.StartLine {
			return nil, fmt.Errorf("símbolo %d (%s/%s): líneas %d-%d inválidas", i, s.File, s.Symbol, s.StartLine, s.EndLine)
		}
	}
	return rep.Symbols, nil
}

// buildSymbolsArgs extiende el run del sandbox con --symbols: el CLI la
// espera después del workdir (`analyzer /workdir [--symbols]`).
func buildSymbolsArgs(workdir string, cfg Config) []string {
	return append(buildRunArgs(workdir, cfg), "--symbols")
}

// ExtractSymbols corre el analyzer en modo --symbols sobre workdir y devuelve
// las filas validadas. Misma política de sandbox y timeout que Run (§9.4):
// sin red, rootfs read-only, ctx con tope duro de AnalyzerTimeout.
func (r *Runner) ExtractSymbols(ctx context.Context, workdir string) ([]Symbol, error) {
	ctx, cancel := context.WithTimeout(ctx, r.config.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "podman", buildSymbolsArgs(workdir, r.config)...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("analyzer --symbols: %s", truncate(msg, 500))
	}
	return ParseSymbols([]byte(stdout.String()))
}
