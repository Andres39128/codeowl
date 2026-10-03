// Config efectiva del repo para la corrida (§9.5): los defaults del
// dashboard (repo.Language) especializados por review.yaml de la rama base.
// El archivo se lee del MERGE-BASE — nunca del head: leerlo del head dejaría
// al PR escribir sus propias reglas de revisión (inyección). Ausente o
// inválido → defaults con registro: jamás rompe la corrida (§9.5).
package review

import (
	"context"
	"log/slog"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3" // dep: yaml.v3 — parser de referencia; hand-roll YAML no es opción (§9.5)

	"github.com/Andres39128/codeowl/backend/internal/store"
)

// RepoConfig es la config efectiva de la corrida (§9.5): por campo, lo que
// declara review.yaml pisa el default (dashboard o global). Los cuatro
// campos entran al hash de la clave de cache (§9.6).
type RepoConfig struct {
	Language     string   `yaml:"language"`
	Profile      string   `yaml:"profile"` // chill | assertive | strict
	PathFilters  []string `yaml:"path_filters"`
	Instructions string   `yaml:"instructions"`
}

// reviewYAMLFile es la ruta declarativa de config por repo, relativa a la
// raíz del clon.
const reviewYAMLFile = "review.yaml"

// validProfiles es el conjunto cerrado de perfiles de revisión (§9.5).
// Espejo del conjunto de config.reviewProfiles (el pipeline no importa
// config, §4.2).
var validProfiles = map[string]bool{"chill": true, "assertive": true, "strict": true}

// repoConfigFile es la forma cruda del review.yaml: punteros para distinguir
// clave ausente (no pisa el default) de clave presente con valor inválido
// (invalida TODO el archivo — §9.5). Claves desconocidas se ignoran.
type repoConfigFile struct {
	Language     *string  `yaml:"language"`
	Profile      *string  `yaml:"profile"`
	PathFilters  []string `yaml:"path_filters"`
	Instructions *string  `yaml:"instructions"`
}

// loadRepoConfig resuelve la config efectiva: base = idioma del repo
// (dashboard) + perfil global; review.yaml leído del merge-base pisa por
// clave. Archivo ausente (el caso común) o git fallando → defaults; yaml
// roto o valor inválido en una clave conocida → TODO el archivo se ignora
// con registro y la corrida sigue con defaults (§9.5: jamás rompe el job).
func loadRepoConfig(ctx context.Context, workdir, mergeBaseSHA string, repo store.Repository, defaultProfile string) RepoConfig {
	base := RepoConfig{Language: repo.Language, Profile: defaultProfile}
	if base.Language == "" {
		base.Language = defaultChatLanguage // migración: language default 'es' (§3.3)
	}

	raw, err := exec.CommandContext(ctx, "git", "-C", workdir, "show", mergeBaseSHA+":"+reviewYAMLFile).Output()
	if err != nil {
		slog.Info("review: sin review.yaml legible en el merge-base, la corrida sigue con defaults",
			"merge_base", mergeBaseSHA, "error", err)
		return base
	}

	var f repoConfigFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		slog.Warn("review: review.yaml con yaml inválido, se ignora completo (§9.5)", "error", err)
		return base
	}
	if f.Language != nil && strings.TrimSpace(*f.Language) == "" {
		slog.Warn("review: review.yaml con language vacío, se ignora completo (§9.5)")
		return base
	}
	if f.Profile != nil && !validProfiles[strings.TrimSpace(*f.Profile)] {
		slog.Warn("review: review.yaml con perfil fuera del conjunto cerrado, se ignora completo (§9.5)",
			"profile", strings.TrimSpace(*f.Profile))
		return base
	}

	eff := base // las claves ausentes quedan en el default
	if f.Language != nil {
		eff.Language = strings.TrimSpace(*f.Language)
	}
	if f.Profile != nil {
		eff.Profile = strings.TrimSpace(*f.Profile)
	}
	eff.PathFilters = f.PathFilters
	if f.Instructions != nil {
		eff.Instructions = *f.Instructions
	}
	return eff
}

// matchPath evalúa los path_filters del review.yaml contra la ruta del
// archivo (§9.5). Lista vacía → true (pasa todo). Reglas en orden: patrón
// plano incluye, `!patrón` excluye; con reglas include el default es
// excluir lo no incluido. La ÚLTIMA regla que matchea gana (mismo criterio
// que .gitignore). `**` cruza segmentos de ruta, `*` queda dentro de uno.
// ponytail: matcher de segmentos propio — path.Match no soporta `**` y un
// glob de terceros no justifica otra dependencia.
func matchPath(patterns []string, file string) bool {
	decision := true // sin includes, todo pasa salvo lo excluido
	for _, p := range patterns {
		if p = strings.TrimSpace(p); p != "" && !strings.HasPrefix(p, "!") {
			decision = false // hay includes: lo no incluido no pasa
			break
		}
	}
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		exclude := strings.HasPrefix(p, "!")
		if exclude {
			p = p[1:]
		}
		if matchGlob(p, file) {
			decision = !exclude
		}
	}
	return decision
}

// matchGlob compara patrón y archivo por segmentos de ruta.
func matchGlob(pattern, file string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(file, "/"))
}

// matchSegments matchea los segmentos restantes del patrón contra los del
// archivo: `**` consume cero o más segmentos, cada otro segmento del patrón
// debe matchear el del archivo uno a uno.
func matchSegments(ps, fs []string) bool {
	for len(ps) > 0 {
		switch {
		case ps[0] == "**":
			if matchSegments(ps[1:], fs) { // `**` como cero segmentos
				return true
			}
			if len(fs) == 0 {
				return false
			}
			fs = fs[1:] // `**` traga un segmento y sigue
		case len(fs) == 0:
			return false
		default:
			if !matchOne(ps[0], fs[0]) {
				return false
			}
			ps, fs = ps[1:], fs[1:]
		}
	}
	return len(fs) == 0
}

// matchOne matchea UN segmento: `*` matchea cualquier subcadena vacía o no
// dentro del segmento — jamás cruza `/`.
func matchOne(pattern, s string) bool {
	for len(pattern) > 0 {
		if pattern[0] == '*' {
			for i := 0; i <= len(s); i++ {
				if matchOne(pattern[1:], s[i:]) {
					return true
				}
			}
			return false
		}
		if len(s) == 0 || s[0] != pattern[0] {
			return false
		}
		pattern, s = pattern[1:], s[1:]
	}
	return len(s) == 0
}
