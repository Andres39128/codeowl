package review

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// Límites defensivos del validador (§9.8): un LLM desbocado que escupe un
// diagrama kilométrico no debe colgar ni inflar el pipeline. Cotas generosas
// para diagramas reales.
// ponytail: constantes fijas acá; a config solo si un PR real las pisa.
const (
	mermaidMaxLines   = 200
	mermaidMaxLineLen = 500
)

// mermaidKinds son los encabezados de diagrama que Mermaid reconoce, casados
// contra el primer token del encabezado (no prefix: `graphTD` no es un
// encabezado válido). stateDiagram-v2 es la variante vigente.
var mermaidKinds = map[string]bool{
	"sequenceDiagram": true, "graph": true, "flowchart": true,
	"stateDiagram": true, "stateDiagram-v2": true, "classDiagram": true,
	"erDiagram": true, "gantt": true, "pie": true, "mindmap": true,
	"timeline": true,
}

// mermaidArrows son las flechas de mensaje de sequenceDiagram: sólida o
// punteada, con o sin cruce de activación, sync/async y con aspas/círculo.
var mermaidArrows = []string{"-->>", "-->", "--x", "--)", "->>", "->", "-x", "-)"}

// validMermaid valida estructuralmente el diagrama (§9.8). Ver cleanMermaid.
func validMermaid(s string) bool {
	_, err := cleanMermaid(s)
	return err == nil
}

// cleanMermaid valida el diagrama y devuelve el contenido sin el cerco
// ```mermaid de transporte que a veces agrega el LLM: publication.go siempre
// lo reenvuelve, y un cerco interno anidado rompería el render (§9.5).
// sequenceDiagram — la salida primaria del summarizer (§6 F3) — se valida
// línea a línea; los demás tipos quedan en chequeo de encabezado solamente:
// el render final lo hace el VCS en el PR y este guard corta basura evidente,
// no reimplementa un renderer Mermaid (§9.8).
func cleanMermaid(s string) (string, error) {
	s = stripMermaidFence(s)
	lines := strings.Split(s, "\n")
	if len(lines) > mermaidMaxLines {
		return "", fmt.Errorf("demasiadas líneas: %d > %d", len(lines), mermaidMaxLines)
	}
	for i, l := range lines {
		if len(l) > mermaidMaxLineLen {
			return "", fmt.Errorf("línea %d excede %d caracteres", i+1, mermaidMaxLineLen)
		}
	}
	idx := slices.IndexFunc(lines, func(l string) bool { return strings.TrimSpace(l) != "" })
	if idx < 0 {
		return "", errors.New("diagrama vacío")
	}
	head := strings.TrimSpace(lines[idx])
	if head == "sequenceDiagram" {
		if err := validSequence(lines[idx+1:]); err != nil {
			return "", err
		}
		return s, nil
	}
	tok := strings.Fields(head)[0]
	if tok == "sequenceDiagram" || !mermaidKinds[tok] {
		return "", fmt.Errorf("encabezado de diagrama desconocido: %q", head)
	}
	// Tradeoff documentado: para tipos no-sequence el encabezado alcanza.
	// `graph` solo, sin cuerpo, pasa: el VCS renderiza más de lo que
	// validamos y el guard no es un renderer.
	return s, nil
}

// stripMermaidFence quita el cerco ```mermaid / ``` solo cuando envuelve todo
// el contenido; un cerco suelto a media cadena queda y el validador lo
// rechaza como línea basura.
func stripMermaidFence(s string) string {
	s = strings.TrimSpace(s)
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s
	}
	first, last := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[len(lines)-1])
	if last == "```" && (first == "```mermaid" || first == "```") {
		return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
	}
	return s
}

// validSequence valida el cuerpo de un sequenceDiagram línea a línea:
// participantes (explícitos o implícitos por primer uso), mensajes, notas,
// activaciones y bloques alt/opt/loop/par/critical/break con apilado de end.
// Iterativo (pila explícita): anidamiento profundo no revienta la pila de Go.
func validSequence(lines []string) error {
	known := map[string]bool{}
	var stack []string
	content := false
	for _, raw := range lines {
		l := strings.TrimSpace(raw)
		// `%%` comenta solo la línea completa: en el texto de un mensaje es
		// texto común (§ dispatch: los mensajes caen al default).
		if l == "" || strings.HasPrefix(l, "%%") {
			continue
		}
		content = true
		f := strings.Fields(l)
		switch f[0] {
		case "title": // título: texto libre
		case "autonumber":
			if len(f) > 1 {
				if len(f) > 2 {
					return fmt.Errorf("autonumber: argumentos de más: %q", l)
				}
				if _, err := strconv.Atoi(f[1]); err != nil {
					return fmt.Errorf("autonumber: número inválido %q", f[1])
				}
			}
		case "participant", "actor":
			id, err := aliasID(l, f[0])
			if err != nil {
				return err
			}
			known[id] = true
		case "activate", "deactivate":
			rest := strings.Fields(strings.TrimSpace(strings.TrimPrefix(l, f[0])))
			if len(rest) != 1 {
				return fmt.Errorf("%s espera un participante: %q", f[0], l)
			}
			if !known[rest[0]] {
				return fmt.Errorf("%s de participante desconocido %q", f[0], rest[0])
			}
		case "note":
			if err := validNote(l, known); err != nil {
				return err
			}
		case "alt", "opt", "loop", "par", "critical", "break":
			stack = append(stack, f[0])
		case "else": // solo dentro de alt
			if len(stack) == 0 || stack[len(stack)-1] != "alt" {
				return errors.New("else fuera de un bloque alt")
			}
		case "and": // solo dentro de par
			if len(stack) == 0 || stack[len(stack)-1] != "par" {
				return errors.New("and fuera de un bloque par")
			}
		case "option": // solo dentro de critical
			if len(stack) == 0 || stack[len(stack)-1] != "critical" {
				return errors.New("option fuera de un bloque critical")
			}
		case "end":
			if len(stack) == 0 {
				return errors.New("end sin bloque abierto")
			}
			stack = stack[:len(stack)-1]
		default:
			if err := validMessage(l, known); err != nil {
				return err
			}
		}
	}
	if len(stack) > 0 {
		return fmt.Errorf("bloque %q sin end", stack[len(stack)-1])
	}
	if !content {
		return errors.New("sequenceDiagram sin contenido")
	}
	return nil
}

// validMessage valida `A<flecha>B : texto` — con o sin espacios alrededor de
// la flecha; el texto puede quedar vacío: el `:` es lo obligatorio. Declara
// participantes implícitos: mermaid los autodeclara al primer uso.
func validMessage(l string, known map[string]bool) error {
	i := strings.Index(l, ":")
	if i < 0 {
		return fmt.Errorf("mensaje sin dos puntos: %q", l)
	}
	sender, receiver, ok := splitArrow(strings.TrimSpace(l[:i]))
	if !ok {
		return fmt.Errorf("mensaje malformado: %q", l)
	}
	known[sender] = true
	known[receiver] = true
	return nil
}

// splitArrow parte `A->>B` en remitente y receptor: la flecha es la primera
// que aparece en la cadena (a igual posición gana la más larga, así `->>`
// nunca queda mordido por `->`) y ambos lados son ids válidos.
func splitArrow(head string) (string, string, bool) {
	best, arrow := -1, ""
	for _, a := range mermaidArrows {
		if i := strings.Index(head, a); i >= 0 && (best < 0 || i < best || (i == best && len(a) > len(arrow))) {
			best, arrow = i, a
		}
	}
	if best < 0 {
		return "", "", false
	}
	sender := strings.TrimSpace(head[:best])
	receiver := strings.TrimSpace(head[best+len(arrow):])
	if !mermaidID(sender) || !mermaidID(receiver) {
		return "", "", false
	}
	return sender, receiver, true
}

// mermaidID: id de participante — letras, dígitos, `_`, `-` y `.`. Corta
// basura tipo `A->*B` que otherwise pasaría como flecha `->`.
func mermaidID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '_' && r != '-' && r != '.' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// validNote valida `note left of A: t`, `note right of A: t` y
// `note over A[,B]: t`; los participantes deben ser conocidos (declarados o
// ya usados en un mensaje) y el `:` es obligatorio.
func validNote(l string, known map[string]bool) error {
	rest := strings.TrimSpace(strings.TrimPrefix(l, "note"))
	for _, pre := range []string{"left of ", "right of ", "over "} {
		if !strings.HasPrefix(rest, pre) {
			continue
		}
		targets, _, ok := strings.Cut(rest[len(pre):], ":")
		if !ok {
			return fmt.Errorf("nota sin dos puntos: %q", l)
		}
		for _, t := range strings.Split(targets, ",") {
			t = strings.TrimSpace(t)
			if !known[t] {
				return fmt.Errorf("nota sobre participante desconocido %q", t)
			}
		}
		return nil
	}
	return fmt.Errorf("nota malformada: %q", l)
}

// aliasID extrae el id de `participant A as Label` / `actor A as Label`:
// id obligatorio (charset de mermaidID), `as Label` opcional y con texto libre.
func aliasID(l, kw string) (string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(l, kw))
	id, _, _ := strings.Cut(rest, " as ")
	f := strings.Fields(id)
	if len(f) != 1 || !mermaidID(f[0]) {
		return "", fmt.Errorf("%s espera un id: %q", kw, l)
	}
	return f[0], nil
}
