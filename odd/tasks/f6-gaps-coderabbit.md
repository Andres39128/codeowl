# Fase 6 — Gaps CodeRabbit (slop, change stack, release notes, triage esfuerzo/impacto)

**Objective:** cerrar los 4 gaps detectados al contrastar codeowl con el resumen de funciones de CodeRabbit (`docs/resumen_funciones_coderabbit.md`). Slack/Discord queda fuera por decisión (chat solo en PR, coherente con self-hosted single-org). Guía primero: §3.3, §9.8 y §6 se actualizan antes del código.

**Problem:** CodeRabbit tiene 5 funciones que codeowl no cubre; el usuario autorizó integrar 4 (descartó Slack/Discord). Base: rama `f6-gaps-coderabbit` desde `fix/river-job-timeout` (lleva fixes del pipeline que F6 toca).

## Specs

- **S1:** "Revisa el documento /home/andres/Dev/Dev_Personal/codeowl/docs/resumen_funciones_coderabbit.md y verifica que le falta al desarrollo que estamos haciendo" — gaps identificados y reportados.
- **S2:** "Integremos los gaps faltantes en está sesión" (verbatim, con su typo) — autorización de implementación.
- **S3:** Alcance elegido en cuestionario: "Los 5 gaps faltantes" (slop detection, change stack, release notes en resumen, Slack/Discord, triage por esfuerzo/impacto).
- **S4:** Slack/Discord: "No, chat solo en PR" — fuera de alcance; los otros 4 gaps entran.
- **S5:** Slop detection — detectar código de relleno/baja calidad generado por IA y publicarlo como findings con nueva categoría `slop` del conjunto cerrado (§3.3); agente best-effort rol `cheap` (patrón Verifier: sin proveedor → se omite con nota declarada, §9.6); nunca bloquea la corrida; los hallazgos pasan por el Verifier como todo `source=llm`.
- **S6:** Release notes — el Summarizer agrega campo opcional `release_notes` (borrador de notas de lanzamiento, markdown); nueva sección del resumen publicada (patrón `premergeSection`), degradada igual que el resto (§9.6/§9.8).
- **S7:** Triage esfuerzo/impacto — `effort_score` e `impact_score` 0-100 transparentes (pesos como consts documentadas, sin config nueva salvo necesidad probada); impacto usa reachability inversa del grafo de imports (F4, blast radius); persistidos por corrida junto a `risk_score`; expuestos en `/api/prs` (sort `effort`/`impact`) y columnas del dashboard de triage; null cuando no hay index del repo (degrada visible).
- **S8:** Change stack — cohortes de dependencia de los archivos cambiados (orden topológico por imports del repo revisado; tests al final por heurística de ruta); persistidas por PR (JSONB nullable, última corrida gana — mismo régimen que risk_score); expuestas en detalle de PR (API + Card nueva en dashboard); best-effort, null sin index.
- **S9:** Regla guía-manda: `docs/guia_del_proyecto.md` (§3.3 taxonomía+columnas, §9.8 agente/schema, §6 Fase 6) y `docs/mapa_arquitectura.yaml` actualizados en el mismo cambio, antes del código.
- **S10:** Verificación — test RED por regla nueva donde aplique test determinista; unit en `internal/review` (stubGateway por markers) + e2e `internal/integration` estilo F5 (tag `integration`, DATABASE_URL, stub LLM OpenAI-compatible); suite completa + build de dashboard al cierre; cobertura declarada como el resto.

## Tasks

- **T0** [S9] docs: HECHA — commit 55a7aa0a04ba. guía §6 Fase 6 + §3.3 (`slop`, effort/impact/change_stack) + §9.8 (SlopDetector, `release_notes`) + mapa_arquitectura.yaml; commit del doc `docs/resumen_funciones_coderabbit.md` — inline — commit `docs(odd)`
- **T1** [S5] slop: HECHA — commit bccc5791d4aa (302 líneas; slice 1 = T0+T1, 636 desde origin/main). `prompts/slop_system.md` + embed.go (embed+promptFiles) + agents.go (marker, runSlopDetector, validCategory `slop`) + review.go wiring pos-dedup + migration 0006 CHECK categoría + label dashboard — inline — commit `feat(review)`
- **T2** [S6] release notes: HECHA — commit 4032bc165236 (campo opcional, sección en ambas fases). summarizer_system.md + parseSummary + publication.go sección + finalSummary (ambos call sites) — inline — commit `feat(review)`
- **T3** [S7] triage scores: risk.go ComputeEffort/ComputeImpact + index dependentsOf (matchImport semantics) + migration 0006 columnas + store query + sqlc regen + review.go persist + api sort/prView + dashboard columnas — inline — commit `feat(api)`+`feat(dashboard)`
- **T4** [S8] change stack: cohorts (topológico + heurística tests) + persist JSONB + handlePRDetail + Card en PrDetail — inline — commit `feat(review)`+`feat(dashboard)`
- **T5** [S10] cierre: e2e `f6_gaps_test.go` + suite completa + sqlc/build checks + README estado + mapa final — inline — commit `test(integration)`+`docs`

## Log

- **L1** (2026-10-23): "Revisa el documento /home/andres/Dev/Dev_Personal/codeowl/docs/resumen_funciones_coderabbit.md y verifica que le falta al desarrollo que estamos haciendo" — reporte entregado: ✅ cubierto (F0-F5), ◐ parciales, ❌ gaps: slop detection, change stack, release notes, Slack/Discord, triage esfuerzo/impacto.
- **L2** (2026-10-23): "Integremos los gaps faltantes en está sesión" — autorización.
- **L5** (2026-10-23): T2 cerrada.
- **L4** (2026-10-23): T1 cerrada; corte de slice T0+T1 registrado (~636 líneas desde origin/main, incluye 3 fixes previos de fix/river-job-timeout ya en la rama base).
- **L3** (2026-10-23): Cuestionario: alcance "Los 5 gaps faltantes"; Slack/Discord "No, chat solo en PR" → 4 features. Decisión de rama: `f6-gaps-coderabbit` desde `fix/river-job-timeout` (3 fixes sin mergear; F6 depende del pipeline corregido). Exploración: reverse imports no existe (construir), migración siguiente 0006, filtros /api/prs en Go, `finalSummary` con 2 call sites.
