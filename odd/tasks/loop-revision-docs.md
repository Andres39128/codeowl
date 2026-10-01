# Loop de revisión de documentación (sin engram, con verificación web)

**Objetivo:** revisar los docs desde cero (sin leer engram ni hallazgos previos), verificar
claims contra fuentes web actuales, aplicar correcciones y repetir hasta no encontrar hallazgos.

**Ruta:** directa inline — ediciones mecánicas de 1 línea, contenido ya comprendido.

## Verificación web (iteración 1) — todos OK

| Claim doc | Verificado | Resultado |
|-----------|-----------|-----------|
| Go 1.27 | go.dev | go1.27.1 (2026-08-28) ✓ |
| PostgreSQL 18.6 / PG19 Beta 4 | postgresql.org | 18.6 (2026-08-13), Beta 4 (2026-09-24) ✓ |
| River 0.x | github.com/riverqueue/river | v0.48.0 ✓ |
| Podman 6 | github.com/containers/podman | v6.1.3 ✓ |
| Tailwind v4.3 | tailwindcss.com/blog | v4.3 ✓ |
| Firma GitLab GA 19.1 (Standard Webhooks, whsec_, v1,{base64}) | docs.gitlab.com | GA 19.1, formato exacto ✓ |
| Drafts GitLab: `Draft:`, `[Draft]`, `(Draft)` | docs.gitlab.com | exacto ✓ |
| CodeRabbit: profiles quiet/chill/assertive, auto_title_placeholder, path_filters, chat.auto_reply | docs.coderabbit.ai | exacto ✓ |
| pgvector/pgvector:pg18 | hub.docker.com | tag existe ✓ |
| GitLab suggested changes tier | docs.gitlab.com | Free ✓ |
| Contrastes WCAG tokens §5.1 (18 pares) | script propio | todos cumplen claims ✓ |

## Iteración 1 — hallazgos y estado

- [x] H1 manual §2.1: stack/internos de CodeRabbit presentados como hecho; marcar como
      inferidos de material público (solo el esquema de config es doc oficial).
- [x] H2 guía §3.6: identidad de staleness (head_sha, base_sha=tip) vs diff real (merge-base) —
      aclarar por qué el ancla es el tip (viene en payload, sin clonar) y que un avance de base
      puede causar re-review espuria segura; cache §9.6 usa merge-base.
- [x] H3 guía §3.5: filtro de autores fuera de org — GitHub tiene author_association; GitLab no:
      documentar mecanismo (membresía de proyecto con cache).
- [x] H4 guía §3.4: estado de must_change_password del admin seed no especificado.
- [x] H5 guía §3.5: comentario en PR pre-conexión — comportamiento no especificado (upsert +
      chat responde; review sigue reactiva).
- [x] H6 mapa: api.expone falta GET /healthz (F0 checklist lo exige).
- [x] H7 mapa: `--pids-limit=64` literal vs regla de config (guía §9.4/§4.2) → `<config>`.

## Iteración 2 — 2 hallazgos (5 ediciones)

- [x] H8 "fase 1/2" de la publicación colisionaba con las fases del proyecto — desambiguado
      en §3.6.2, §9.6 (×2) y §9.7 ("fase N de la publicación (§6)").
- [x] H9 §3.3 "clave master de §9" → "master key de §9.2" (ref precisa + terminología).

## Iteración 3 — convergencia

- [x] Pasada final sin hallazgos: YAML del mapa válido (9 claves raíz), sin "fase N" ambiguo
      restante, links del README resuelven, referencias § internas completas.

**Convergencia declarada a la iteración 3.** Commit de cierre: rama `docs/web-verified-review-loop`.

