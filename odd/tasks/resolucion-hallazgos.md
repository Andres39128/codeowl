# Resolución de hallazgos entre corridas

Header: marcar qué hallazgos quedaron resueltos (desaparecieron de la corrida
nueva) y cuáles persisten, en el detalle del PR y en las métricas. La
resolución es una vista computada (como `dismissed`): los findings históricos
no se tocan (auditoría §3.3).

## Specs

- S1: "como se que un hallazgo quedo resuelto o no?" — el sistema debe
  responder explícitamente esta pregunta; hoy la resolución es implícita
  (la corrida nueva simplemente no trae el hallazgo) y no hay comparación.
- S2: elección del usuario "Comparación + métricas": "Badge persistente +
  sección Resueltos (N) colapsada en el detalle del PR. Comparación contra
  la corrida anterior del mismo PR."
- S3: "tasa de resolución en la página de métricas (resueltos vs
  persistentes por repo/semana)."
- Huella de comparación: (repository, file, category) — el MISMO match que
  usan los dismissals (0010) y el metrics job (§6 F5). Una huella presente
  en ambas corridas = persistente; presente solo en la anterior = resuelto.
- Los hallazgos con huella dismissada ("no aplica") no cuentan como
  resueltos ni como persistentes: ya tienen su veredicto humano.
- `resolved`/`persistent` solo se computan para la corrida más reciente del
  PR (la que el usuario está mirando por defecto).

## Tasks

- T1 (S1, S2): backend — query del previo por PR + `persistent` en
  findingView + `resolved[]` en GET /api/prs/{id} + tests. [87e065f1]
- T2 (S2): dashboard — badge "Persistente" + sección colapsada "Resueltos
  en esta corrida (N)" en PrDetail + tests. [pending]
- T3 (S3): backend — ListResolutionPerWeek (por repo/semana, excluye
  huellas dismissadas) + extensión de métricas + tests. [c16b7476] (fix colateral: DeleteRepositoryCascade + finding_dismissals [796bf327])
- T4 (S3): dashboard — tasa de resolución en la página de métricas + tests. [fa8d86a6]
- T5 (S5): docs — guía §3.3 (ciclo de vida del hallazgo) + mapa
  arquitectura + log de cierre. [1921f192]

## Log

- L1 (pedido original, verbatim): "Ok hay que hacer más ajustes, como se que
  un hallazgo quedo resuelto o no? ejempo este "● Alta Estilo SAST
  Determinista (SAST) analyzer/src/test/testdata/app.js:3 No aplica
  Arreglar con IA 'path' is assigned a value but never used. (regla:
  eslint/no-unused-vars)""
- L2: el usuario eligió "Comparación + métricas" sobre la pregunta de
  alcance (badge persistente + resueltos colapsados + tasa de resolución en
  métricas).
- L3: verificación — backend: suite completa ok (api/store/review…), solo
  falla TestCancelPendingByPR, PREEXISTENTE en main limpio (verificado con
  stash); dashboard: 127/127. RED→GREEN por tarea. Fix colateral:
  DeleteRepositoryCascade no borraba finding_dismissals (FK 0010).
- L4: sqlc no rastrea columnas de window functions (LAG) en CTEs —
  ListResolutionPerWeek usa self-join + NOT EXISTS para el predecesor
  inmediato. La tasa por fila viaja como float (denominador ≥ 1 siempre).
- L5: preexistente en dashboard: biome marca 2 errores a11y en
  PrDetail.tsx L741 (role="group" redundante en details, S4) — están en
  main; reportado al usuario, fuera del alcance de este feature.
- L6: entrega — PRs encadenadas a pedido del usuario: #18 (esta rama →
  main, ~876 líneas) y #17 (trazabilidad-merges apilada sobre esta).
  Orden de merge: #18 → #17.
