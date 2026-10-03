// Package prompts embebe los system prompts versionados de los agentes
// (guía §9.5: los prompts son código — cambio de prompt = PR con
// justificación, igual que cualquier cambio). Jamás strings hardcodeados en
// Go: los archivos .md de este directorio son la única fuente.
package prompts

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
)

// FS contiene los system prompts versionados.
//
//go:embed reviewer_system.md summarizer_system.md chat_system.md
var fs embed.FS

// Reviewer devuelve el system prompt del agente Reviewer (revisión por
// archivo, salida JSON de findings).
func Reviewer() string { return mustRead("reviewer_system.md") }

// Summarizer devuelve el system prompt del agente Summarizer (resumen del
// PR, salida JSON con summary/walkthrough/mermaid).
func Summarizer() string { return mustRead("summarizer_system.md") }

// Chat devuelve el system prompt del agente Chat (respuestas a menciones
// @bot, salida en texto markdown — §6 F2). El hueco {{LANGUAGE}} lo llena
// el caller con el idioma configurado del repo (§3.3).
func Chat() string { return mustRead("chat_system.md") }

// promptFiles son los archivos que componen la versión del prompt.
var promptFiles = []string{"reviewer_system.md", "summarizer_system.md", "chat_system.md"}

// Version es el hash del contenido de todos los prompts: es la "versión del
// prompt" de la clave de cache (§9.6) — cambiar un prompt invalida la cache
// sin tocar ningún constante.
func Version() string {
	h := sha256.New()
	for _, name := range promptFiles {
		f, err := fs.Open(name)
		if err != nil {
			// Imposible: el embed garantiza la existencia del archivo.
			continue
		}
		_, _ = io.Copy(h, f)
		_ = f.Close()
	}
	return hex.EncodeToString(h.Sum(nil))
}

func mustRead(name string) string {
	b, err := fs.ReadFile(name)
	if err != nil {
		// Imposible con go:embed: el archivo está compilado dentro del
		// binario. Un pánico acá delata un nombre mal tipeado arriba.
		panic("prompts: leyendo " + name + ": " + err.Error())
	}
	return string(b)
}
