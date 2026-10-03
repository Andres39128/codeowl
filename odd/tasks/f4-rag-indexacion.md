# Fase 4 — RAG: Indexación y Contexto Simbólico

## Objetivo

Implementar F4 según la guía §6: `IndexJob` con chunking simbólico (tree-sitter vía CLI analyzer), embeddings a pgvector, grafo de imports, retrieval para el Reviewer, re-indexación incremental y anclas de dedup por símbolo contenedor.

## Entregable (guía)

El Reviewer recibe contexto simbólico del repo (símbolos relacionados vía pgvector + grafo de imports) en lugar de solo el diff.

## Alcance autorizado

- Checklist F4 §6 completo (4 ítems).
- Branch `fase-4` desde fase-3 (efe3dc9f).
- Merge y push: EXPLÍCITAMENTE diferidos por el usuario hasta terminar todas las fases.

Fuera de alcance: F5 (triage, risk score, Pre-merge, MetricsJob, descarte de jobs al cierre del PR — gap pre-existente detectado en exploración, se registra para F5).

## Realidad vs mapa (exploración 2026-10-03 — esto manda el diseño)

1. **Tree-sitter NO existe**: analyzer CLI es stdlib puro (go.mod sin deps, `CGO_ENABLED=0` en Containerfile). El modo `--symbols` es un stub que emite `{path, language}`. F4 agrega bindings Go de tree-sitter + gramáticas + queries por lenguaje, pins en go.mod (§9.4), CGO en el build del Containerfile. cgo NO sale del contenedor (§3.2): el backend nunca importa tree-sitter.
2. **Gateway sin embeddings**: hay que agregar `Embed` (rol `embedding`, endpoint OpenAI-compatible `/v1/embeddings`) reutilizando failover/backoff/Retry-After/semáforos/llm_usage.
3. **`internal/index` no existe**; `repo_index` no existe (migración 0004: `CREATE EXTENSION vector` + tabla + índice HNSW).
4. **Presupuestos de tokens no existen como código** (solo límites de concurrencia/líneas): el "tope propio por IndexJob" (§9.6) se materializa como tope de símbolos por corrida (config) — proxy honesto y computable antes de gastar.
5. **El re-encolo transaccional de §3.6.1.5 NO está implementado** (gap pre-existente de F1, documentado por el explorador): el trigger de IndexJob al completar review aterriza secuencial en `ReviewJobWorker.Work` tras `review.Run` — consistente con el código actual.
6. **El cierre/merge del PR no descarta jobs ni encola nada** (gap pre-existente): F4 agrega SOLO el enqueue de IndexJob on merge (checklist F4); descarte y MetricsJob quedan para F5.
7. **Anchor de dedup guarda la línea**: `comments_sent.anchor` es `strconv.Itoa(line)`; `IsDuplicate` salta anchors no-numéricos (punto de extensión natural para símbolos).

## Decisiones de diseño

1. **Dims del embedding**: `vector(1536)` fijo en DDL (§3.3: "pgvector fija la dimensión en DDL"; 1536 = default más común OpenAI-compatible). Las dims vigentes se LEEN del catálogo de PG (`pg_attribute` typmod de `repo_index.embedding`) — config nunca diverge del DDL. Proveedor con dims distintas → bloqueado en settings (§3.3); cambio de dims = migración nueva (documentado en el error).
2. **Detección de lenguaje**: queda extension-switch (go|js|py|rust) — go-enry NO entra (la ganancia no justifica la dependencia; desviación del mapa documentada y corregida en el mapa).
3. **Symbols por lenguaje**: tree-sitter queries mínimas por lenguaje: functions, methods, classes/types/structs; `imports` por archivo (Go: `import` decl; JS/TS: `import`/`require`; Python: `import`/`from`; Rust: `use`). Salida: `{file, symbol, kind, start_line, end_line, imports}[]` (shape del mapa).
4. **Presupuesto y resumen de IndexJob**: tope `INDEX_MAX_SYMBOLS_PER_RUN` (default 2000); resumen por `file_hash` — archivo con hash igual y mismo `embedding_model` ya indexado se salta (retoma natural, §9.6 "se retoma en el próximo IndexJob"). Orden determinístico. Cambio de proveedor embedding (modelo distinto, dims iguales por guard de settings) → wipe del repo + re-index completo.
5. **IndexJob**: kind nuevo `index`, cola `review` (comparte slots, §9.6), unique ByArgs(repo)+ByState sin completed (misma mecánica que ReviewJob). Triggers: (a) fin de review success|partial (en `ReviewJobWorker.Work` post-Run), (b) conectar repo (POST /api/repos) y reconectar (PUT enabled), (c) merge del PR (handlers de cierre que registran merged_at). Sin proveedor embedding enabled → NO se encola (log estructurado, §9.6).
6. **Retrieval para el Reviewer**: por archivo revisado, query = path + hunks (truncado) → 1 llamada embed (rol embedding) → top-K coseno (`<=>`) del repo excluyendo el mismo archivo → expansión 1 salto por grafo de imports file-level (match best-effort de import-string contra paths del repo) → bloque "Símbolos relacionados" en el user prompt (después del diff), tope `REVIEW_CONTEXT_MAX_CHARS` (default 4000). Índice vacío → sin llamada ni bloque (silencioso). Falla de embedding en retrieval → sin contexto + log (la review NUNCA se degrada por indexar §9.6). La cache de resultados NO gana la recuperación a su key (la guía fija la key: head+merge-base+prompt+config efectiva).
7. **review.Run gana un Retriever**: interfaz propia en review (`Retriever.RetrieveRelated(ctx, repoID, file, code string) []RelatedSymbol`), parámetro nuevo de Run; nil o vacío → sin contexto. review no importa index (map: index importa analyze/llm/store; review depende de index — con interfaz en review el acoplamiento apunta hacia adentro, workers cablea el real).
8. **Anclas de dedup**: al cerrar findings, `Runner.ExtractSymbols(workdir)` (2ª invocación del analyzer con `--symbols`) → finding (file,line) → símbolo contenedor → `comments_sent.anchor` = nombre del símbolo; sin símbolo contenedor → fallback a línea con drift ±3 (cfg.DriftLines). `IsDuplicate` dual: anchor no-numérico (símbolo) matchea por igualdad exacta; anchors numéricos legacy matchean por drift (transición sin republish masivo).
9. **repoconfig se extrae a `internal/repoconfig`**: `loadRepoConfig`+`matchPath`+`RepoConfig` pasan de review a paquete propio — review e index lo importan (index respeta los `path_filters` vigentes leyendo review.yaml del tip de la rama por defecto clonada). Cero duplicación (§4.3).
10. **Settings valida dims** (§3.3): create/update de provider rol embedding+enabled → probe de dims (1 llamada embeddings) contra dims del catálogo → mismatch = 422 con mensaje claro (primera habilitación con índice vacío pasa y define). TestConnection de rol embedding pasa a probe embeddings.

## Tareas

- [x] T1 — feat(analyzer): tree-sitter bindings + modo --symbols real (4 lenguajes: symbol/kind/start/end + imports) + fixtures + CGO/pins en Containerfile + justfile test wiring (ruta: delegado)
  - Commit a7f2d2638442: symbols.go (331) + symbols_test.go (220) + fixtures testdata/, +723/−25, 9 tests nuevos, podman build validado dentro del sandbox
  - Pins: go-tree-sitter v0.24.0 + gramáticas tree-sitter-* v0.23.x (TRAMPA ABI: tags v0.25 requieren ABI 15, runtime Go aún 13-14 — verificado empíricamente)
  - Contrato: symbols[] {file, symbol, kind, start_line, end_line, imports[]} (imports file-level, deduped); archivo roto → 0 rows sin fallar; .ts vía grammar JS (limitación documentada)
- [x] T2 — feat(llm): Embed rol embedding (/v1/embeddings, failover reutilizado, llm_usage, probe dims) + TestConnection embedding (ruta: delegado)
  - Commit de2d36fcff71: gateway +220, openai +96, 9 tests nuevos (+race ok), +617/−25
  - API: Embed(ctx, texts) [][]float32 / TestEmbedConnection(ctx, baseURL, apiKey, model) (dims, latency, err); EMBED_BATCH_SIZE default 64 (Limits); TestConnection role=embedding rutear embeddings sin romper LLMTester
- [x] T3 — feat(store): migración 0004 (extension vector, repo_index + HNSW) + queries (upsert/delete/wipe/dims del catálogo/retrieval coseno) + extracción internal/repoconfig (ruta: delegado)
  - Commit fc7d50f28afd: 0004_repo_index.sql (IDENTITY, vector(1536), HNSW cosine), 8 queries generadas, repoconfig extraído (git detecta renames), +845/−145, BD real verde
  - Notas: sqlc override vector→string (sin dep pgvector-go); INSERT params posicionales Column7/Column10 con COALESCE '{}'; atttypmod==1536 confirmado; GetRepoIndexDims del catálogo
- [x] T4 — feat(index): Index — extract→path_filters→resume por file_hash→embed batch→upsert, wipe on model change, tope símbolos/corrida (ruta: delegado)
  - Commit 257ff19cfbbf: analyze/symbols.go + index.go (349) + 13 tests, +1125; extraction real vía podman+tree-sitter verificada
  - Notas: budget corta ENTRE archivos (nunca a mitad — resume por hash quedaría congelado); dims pre-check determinista vs catálogo; SymbolQueryText "file\nkind symbol" es contrato congelado para T5; imagen analyzer local quedó stale (gotcha ops documentado)
- [x] T5 — feat(index): Retrieve — top-K coseno + expansión 1 salto grafo de imports (ruta: delegado)
  - Commit b0fe24ae5339: retrieve.go (199) + 8 tests (+race), RETRIEVAL_TOP_K default 8, expansión tope topK*2 ordenada (file, start_line, symbol)
  - Notas: RetrieveStore incluye ListRepoIndexFiles (universo de paths para resolver imports); índice vacío → nil sin llamadas; errores propagan (T7 decide skip+log)
- [x] T6 — feat(jobs): IndexJob — kind/cola review/unicidad, worker, triggers (review success|partial, conectar/reconectar repo, merge PR), skip sin proveedor embedding (ruta: delegado)
  - Commit 4aec75b013b7: +663/−66; adapters ya tenían el queue inyectado (New(st, cfg, jq)) → EnqueueIndexJob guard compartido cayó en worker/api/ambos webhooks sin coupling nuevo
  - Notas: uniqueWhileAliveStates renombrado (set compartido review+index); merge ahora SÍ encola (tests viejos actualizados — merge no-encola era aserción F1); worker skip repo disabled + ErrNoEmbeddingProvider sin quemar retries
- [x] T7 — feat(review): contexto simbólico del Reviewer — Retriever param, bloque de símbolos en prompt, tope chars, sin degradación (ruta: delegado)
  - Commit 510fb9616e02: +520/−51, 5 tests context + 2 adapter, race ok; Run ganó param retr (nil-safe); workers cablea NewIndexRetriever
  - Notas: retrieval error → log + sin bloque + review sigue (§9.6); cache key intacta ( guía fija); reviewer_system.md +1 bullet (referencia-only); REVIEW_CONTEXT_MAX_CHARS default 4000 en cfg hash
- [x] T8 — feat(review): anclas de dedup por símbolo contenedor — ExtractSymbols en la review, anchor simbólico, IsDuplicate dual (ruta: delegado)
  - Commit 919ac41c9c69: dedup.go rework + wiring Run/publication/verifier + dedup_test.go (264), 6 archivos, +371/−29
  - API: SymbolExtractor (interfaz opcional, *analyze.Runner la satisface), anchorFor(finding, symbols) (string, bool), IsDuplicate(existing, f, anchor, drift), Fingerprint(file, category, anchor), Finding.Anchor; publication escribe f.Anchor resuelto
  - Notas: fallo/ausencia de extracción → anclas por línea con log estructurado (jamás degrada); consulta numérica o vacía nunca matchea filas simbólicas; copia propia de findings pre-anchor para no mutar el backing array de la cache (§9.6); extracción se salta si no hay hallazgos
- [x] T9 — feat(api)+test: settings valida dims embedding + integration test F4 e2e + mapa/.env.example/justfile docs (ruta: delegado)
  - Commit ba059b99f3b5: guard dims 422 (probe vs catálogo, siempre 1536) + f4_rag_index_test.go (523: indexación con resume/budget + retrieval/anchors e2e) + mapa (go-enry corregido, Embed, dims guard), +762/−10
  - Gate F4: 12 packages backend ok · integración 5/5 (F1+F2+F3+F4×2) · analyzer 11 tests · dashboard 77/77 · just test verde

## Evidencia de progreso

| Tarea | Commit | Verificación |
|-------|--------|--------------|
| T1 analyzer symbols | a7f2d2638442 | 9 tests + smoke + podman build; pins ABI verificados |
| T2 gateway Embed | de2d36fcff71 | 9 tests + race; EMBED_BATCH_SIZE |
| T3 repo_index + repoconfig | fc7d50f28afd | 8 queries, BD real, migración 0004 idempotente |
| T4 index Index | 257ff19cfbbf | 13 tests; extraction real podman+tree-sitter |
| T5 index Retrieve | b0fe24ae5339 | 8 tests + race; expansión imports tope topK*2 |
| T6 IndexJob wiring | 4aec75b013b7 | triggers×4 + guarda sin proveedor; unicidad BD real |
| T7 contexto Reviewer | 510fb9616e02 | 5 tests context + 2 adapter; sin degradación |
| T8 anclas símbolo | 919ac41c9c69 | 30/30 subtests + race; dual-mode legacy |
| T9 settings+e2e+docs | ba059b99f3b5 | guard 422; e2e 5/5; mapa/.env verdes |

## Cierre F4 (verificación final del orchestrator)

- Backend: 12 packages ok. Integración (`-tags integration`, BD real): **5/5 PASS** (incluye F4 indexación + retrieval/anchors e2e). Analyzer: 11 tests. Dashboard: 19 files/77 tests (sin regresión). `just test` integra las tres suites.
- Checklist §6 F4: los 4 ítems materializados; ítems 3 y 4 verificados end-to-end contra BD real; ítems 1 y 2 con el path del worker River cubierto por unit tests (convención de la casa: el analyzer se stubea en integración — la extracción real podman+tree-sitter se validó manualmente en T4/T1) y la expansión del grafo de imports cubierta por unit tests.
- 9 commits work-unit en fase-4 (a7f2d263..ba059b9) + docs(odd) de cierre. Sin push (merge/push diferidos por el usuario hasta cerrar todas las fases).
- Gaps pre-existentes registrados para F5: re-encolo transaccional §3.6.1.5 no implementado; cierre de PR no descarta jobs pendientes ni encola MetricsJob (es ítem F5 igualmente).

## Verificación por tarea (comando canónico)

- Backend: `cd backend && go test -count=1 -p 1 ./...`
- Analyzer: `cd analyzer/src && go test ./...` (nuevo, cableado a `just test`)
- Dashboard: `pnpm --dir dashboard test` (F4 no toca dashboard — regression check)
- Integración: `export $(grep ^DATABASE_URL= .env)` + `go test -tags integration ./internal/integration/`
- Spot-check del orchestrator por tarea

## Modo TDD

Off (convención F1-F3: tests con cada tarea, verde antes del commit).
