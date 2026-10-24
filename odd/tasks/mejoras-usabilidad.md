# Mejoras de usabilidad del sistema

El sistema funciona pero no se explica ni ayuda a actuar. Cuatro unidades
aprobadas por el usuario (todas), en este orden: A salud por repo, B detalle
de corrida, C métricas de valor, D triage de hallazgos.

## Specs

- **S1 — Salud por repo**: tarjeta por repo con checks en vivo: ¿App
  instalada? · ¿webhook llegando? (última entrega en `webhook_deliveries`) ·
  ¿proveedor `review` activo? Botón **"revisar el último PR ahora"** que
  encola la corrida sin esperar webhook. Si un check falla, la UI dice qué y
  cómo arreglarlo. Incluye checklist de onboarding en la conexión.
- **S2 — Detalle de corrida**: desde el PR y desde el historial de la cola,
  ver de una corrida: cobertura (recortes y por qué), proveedor usado por
  rol, tokens consumidos (`llm_usage` por review), duración, y los jobs
  asociados con su resultado.
- **S3 — Métricas de valor**: tendencias visibles: PRs revisados por semana,
  tiempo de ciclo (created→merged de F5), % sugerencias aplicadas y % falsos
  positivos (comments_sent.resolved/applied + findings.accepted).
- **S4 — Triage de hallazgos**: agrupar hallazgos por archivo (colapsable),
  marcar un hallazgo "no aplica" (queda en la base para auditoría, la UI lo
  atenúa y lo oculta por defecto en corridas nuevas por huella), y el estado
  Aplicada/No aplicada visible cuando F5 lo calculó.

## Tasks

- T0 (S1-S4): docs — mapa_arquitectura endpoints nuevos + guía §3.5/§9.9 si
  aplica. [este commit]
- T1 (S1): backend salud por repo + "revisar ahora" + tests. [cd1de85d]
- T2 (S1): dashboard tarjeta de salud + botón + tests. [c7846747]
- T3 (S2): backend GET /api/reviews/{id} + tests. [9bd9238b]
- T4 (S2): dashboard vista de corrida + tests. [39ebabd2]
- T5 (S3): backend serie semanal en /api/metrics + tests. [3b60cc7c]
- T6 (S3): dashboard barras semanales en Triage + tests. [4f0f645b]
- T7 (S4): migración 0010 + dismiss por huella + tests. [70a17655]
- T8 (S4): dashboard agrupación por archivo + no aplica + tests. [1afef440]
- T2 (S1): dashboard tarjeta de salud + botón + tests.
- T3 (S2): backend GET /api/reviews/{id} (resumen, cobertura, uso por rol,
  duración) + tests.
- T4 (S2): dashboard vista de corrida + link desde historial + tests.
- T5 (S3): backend GET /api/metrics/summary + tests.
- T6 (S3): dashboard página de métricas + nav + tests.
- T7 (S4): migración finding_dismissals + endpoints + tests.
- T8 (S4): dashboard agrupación por archivo + marcar no aplica + tests.

## Log

- L1 (pedido original, verbatim): "Ahora, cómo podemos mejorar el sistema
  para que pueda ser más claro y más util"
- L2: el usuario eligió las cuatro unidades (A salud por repo, B detalle de
  corrida, C métricas de valor, D triage de hallazgos) en ese orden.
- L3: las cuatro unidades implementadas y verificadas (backend 12/12 con BD
  real, dashboard 115/115). Commits por tarea arriba.
