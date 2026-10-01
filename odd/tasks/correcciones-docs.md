# ODD Feature — Doc Gap Closure (correcciones-docs)

**Objective:** Apply the 7 documentation gaps + 2 implementation-better-otherwise items from the doc review session, then run a second full sweep to close the circle (consistency + missing-failure hunt) and apply its findings.

**Problem:** First review pass found real gaps: chat execution model undefined, fork clone strategy missing, GitHub App permissions absent, Gitleaks secret leak via comments, review.yaml/dashboard precedence ambiguous, worker concurrency undefined, prompt evals measuring the wrong role, chi unjustified vs stdlib per the project's own dependency rule, accented directory name.

**Scope (authorized):** Edit `docs/guia_del_proyecto.md` and `docs/mapa_arquitectura.yaml` only. `docs/manual_completo_de_coderabbit_ai.md` is a reference of the external product — untouched.

**Constraints:**
- Spanish, present tense, existing doc style (tables/checklists, decision-first per cognitive-doc-design).
- Preserve all existing §x.y cross-references; new text must reference existing sections where the guide already covers a topic.
- No new sections unless an edit list item says so; prefer surgical extension of existing paragraphs/bullets/checklist items.
- Route: delegated direct (one writer — 2 non-trivial files); parent does structural readback (passive docs — no review ceremony).

**Tasks:**

- [x] T1 — Apply 15 enumerated edits to `docs/guia_del_proyecto.md` (writer) — done, zero failed anchors
- [x] T2 — Apply 9 enumerated edits to `docs/mapa_arquitectura.yaml` (writer) — done, 1 YAML quoting deviation (exact text, quoted scalar), file re-validated
- [x] T3 — Structural readback: all markers present, no stray `chi` outside the discarded-alternatives row, cross-refs intact (parent) — done; found and fixed 1 writer leftover (§3.1 diagram `(chi)`), 1 paren imbalance in mapa FetchPR, YAML OK, 39 headings intact
- [x] T4 — Second sweep, close-the-circle pass over final docs; apply any residual findings (parent) — done; 3 consistency fixes applied: ChatJob added to §3.5 PR-close job discard, `priority` order named in guide §9.7 failover, ChatJob (F2) added to mapa jobs `fase` line

**Outcome:** 24 writer edits + 4 parent sweep fixes + 1 writer-caught fix = 29 changes across 2 files. `manual_completo_de_coderabbit_ai.md` untouched. No git repo exists — no work-unit commits; `git init` + ASCII dir rename recommended to user.

**Second-sweep findings to fold into T1/T2 (beyond original 9):**
1. `issue_comment` fires for issues AND PRs → early filter (PR + bot mention); GitLab `note` filter by `noteable_type == MergeRequest` + mention.
2. Chat commands from non-org authors ignored (config) — LLM budget not exposed to third parties on public repos.
3. CSRF scope clarified: session-cookie endpoints only; webhooks authenticate via HMAC signature, no cookie.
4. River's own schema enters via its official migration.
5. `llm_providers.priority` (failover order within role) added.
6. Webhook payload size cap rejected before parsing.
7. Invalid `review.yaml` fails safe: ignored with log, run continues with defaults.
8. Config validation staged: fail-fast only on essentials (DB, master key); VCS creds validated when first repo of that type is connected — F0 runs without VCS credentials.
9. Worker image needs `git` + podman CLI access.
10. Real image is `pgvector/pgvector:pg18` (official postgres image lacks the extension).
11. Chat SLO: soft target P95 < 2 min, async via ChatJob (webhook returns 2xx immediately; never LLM in handler).
12. Chat has its own per-command budget.

**Verification:** grep markers (`ChatJob`, `refs/pull`, `ServeMux`, `pgvector/pgvector`, `priority`, `Metadata: Read`, `fail-fast` staging in mapa config) + parent readback of every changed section. No git repo exists yet — no work-unit commits possible; recommend `git init` + ASCII dir rename (`revision-codigo`) to the user at delivery.

**Route evidence:** Writer trigger fired (2 non-trivial files, ~24 discrete edits). Mapping done in prior turn (3 files read inline — under 4-file threshold).

---

# Batch 2 — Second-pass gap closure (2026-09-30, second review session)

**Objective:** Close the 6 remaining gaps from the second full doc review (post-batch-1 state): SLO/publish-flow contradiction, dashboard safe-rendering rule, VPS deploy story, `reviews.status` closed set, IndexJob trigger policy, default comment language pre-F3.

**Scope (authorized):** Edit `docs/guia_del_proyecto.md` + `docs/mapa_arquitectura.yaml`; delete stale LibreOffice lock file (already gone on arrival — self-resolved); manual untouched.

**Tasks:**

- [x] T1 — 7 edits to `guia_del_proyecto.md` (writer): §6 two-phase SLO paragraph, §3.3 `reviews` status closed set + `comments_sent` typed rows, §9.5 safe-rendering bullet, new §9.13 deploy/upgrade, F1/F4 checklist items — done, all anchors exact
- [x] T2 — 4 edits to `mapa_arquitectura.yaml` (writer): flujo `revisar_pr` restructured to 7 steps with fase 1/fase 2 publication, stale re-check before each publish, IndexJob uniqueness policy in jobs reglas, safe-rendering in theme reglas — done, YAML valid
- [x] T3 — Parent readback: all markers grepped + changed regions read; 11 `##` sections intact; YAML parses — done; 1 residual inconsistency found and fixed by parent (flujo `indexar_repo` still had old IndexJob trigger wording → aligned with new jobs-reglas policy)
- [x] T4 — Housekeeping: `.~lock.guia_del_proyecto.md#` no longer existed (self-resolved); `git init` still pending — re-flagged to user

**Outcome:** 11 writer edits + 1 parent consistency fix = 12 changes across 2 files. Two-phase publication now consistent in 3 places (§6, F1 checklist, mapa flujo). Writer discovery saved: mapa flujo steps containing `: ` parse as single-key mappings (pre-existing convention); SLO numbers (5/15 min) duplicated between §6 and mapa flujo by design — future SLO changes must touch both.

**Verification:** 9 writer checks green (grep markers, section count, YAML safe_load) + parent re-ran grep/yaml spot checks independently.

**Route evidence:** Writer trigger fired again (2 non-trivial files, 11 discrete edits) — one delegated writer; parent structural readback (passive docs, no review ceremony; no git repo → no diff-based assess possible).

---

# Batch 3 — Residual gap closure (2026-09-30, third review session)

**Objective:** Apply the 7 residual findings + 3 one-liners from the third doc review: F4 tree-sitter access path (symbols mode), fork clone ref (merge→head), `edited`/retarget re-review trigger, LLM cache key completeness, F5 coverage input replacement, user management scheduling, pre-merge verdict placement (summary edit, no new comments_sent type), unregistered-repo webhook behavior, summary re-edit after fase 2, FP-metric provenance.

**Scope (authorized):** Edit `docs/guia_del_proyecto.md` + `docs/mapa_arquitectura.yaml`; manual untouched.

**Tasks:**

- [x] T1 — 11 edits to `guia_del_proyecto.md` (writer) — done, 0 failed anchors
- [x] T2 — 8 edits to `mapa_arquitectura.yaml` (writer) — done, 1 justified deviation: `index.proposito` quoted scalar (plain scalar with `: ` is illegal YAML; follows config.proposito precedent), YAML validates
- [x] T3 — Parent readback: all markers grepped independently (guide 170/192/354/378/387/467; mapa 102/113/144/149/155/177/209/249/279), `yaml.safe_load` PASS, `index.deps == [store, llm, analyze]`, headings unchanged — done, 0 residuals
- [x] T4 — Close: outcome + engram mirror update

**Outcome:** 19 edits across 2 files: fork clone head-ref (+ merge-ref rationale) in 2 places, `edited`/retarget filter in 2, cache key + config efectiva in 2, `--symbols`/`ExtractSymbols` F4 path in 4 (guide E1/E9, mapa M2/M3/M4), user mgmt scheduled F1 (guide E8 + mapa settings), password reset (E6), pre-merge verdict as summary edit-in-place (E11 + mapa M6), coverage input → test-files proxy (E10), unregistered-repo webhook 2xx+discard (E4), summary re-edit after fase 2 (E5 + mapa M8), FP metric provenance (E11b). Manual untouched. No git repo — still no work-unit commits; `git init` + ASCII dir rename still recommended.

**Route evidence:** Writer trigger fired (2 non-trivial files, ~19 discrete edits) — one delegated writer; parent structural readback (passive docs; no git repo → no work-unit commits, no diff-based assess).

---

# Batch 4 — Fourth-pass gap closure (2026-09-30, fourth review session)

**Objective:** Close the 5 gaps + 1 micro from the fourth review, mostly second-order effects of Batch 3: corrida identity (head_sha, base_sha) for staleness/re-encolo, pull_requests base columns, LLM cache storage decision, GitLab `update` attribute filter, repo disconnect semantics, comments_sent dedup huella.

**Scope (authorized):** Edit `docs/guia_del_proyecto.md` + `docs/mapa_arquitectura.yaml`; manual untouched.

**Tasks:**

- [x] T1 — 7 edits to `guia_del_proyecto.md` (writer; §3.6.1 full-item replacement) — done, 0 failed anchors
- [x] T2 — 4 edits to `mapa_arquitectura.yaml` (writer; folded blocks reflowed ≤80 cols) — done, YAML validates
- [x] T3 — Parent readback: §3.6.1 read in full context (identity (head_sha, base_sha) coherent through the whole interleaving proof), §3.5 both additions verified, table rows + §9.6 read, mapa semantics 4/4 True via parsed YAML — done, 0 residuals
- [x] T4 — Close: outcome + engram mirror update

**Outcome:** 11 edits across 2 files. Corrida identity = (head_sha, base_sha) now consistent in guide §3.6 intro + §3.6.1 + mapa review/jobs reglas (retarget during in-flight review no longer lost — the hole Batch 3 exposed). `pull_requests` gained base_ref/base_sha; `comments_sent` declares the dedup huella; LLM cache = worker memory + entry cap + TTL (no table, consistent with "Broker/cache: Ninguno"); GitLab `update` filtered by attribute (push/target_branch/draft — title/labels discarded); repo disconnect = `enabled` flag preserving history, reconnect re-indexes via existing F4 connect policy. Manual untouched. Cumulative: 4 batches, 82 changes. No git repo — `git init` + ASCII dir rename still pending (4th flag).

**Verification note:** writer caught a miscount in the parent's own verification spec (expected 3 occurrences of `(head_sha, base_sha)` in §3.6.1; the provided text correctly contains 2) — spec error, not a dropped edit.

**Route evidence:** Writer trigger fired (2 non-trivial files, 11 discrete edits) — one delegated writer; parent structural readback (passive docs; no git repo → no commits, no diff-based assess).

---

# Batch 5 — Fifth-pass gap closure (2026-09-30, fifth review session)

**Objective:** Close the 9 findings + 2 micros of the fifth review: flujo stale-skip still head-only (B4 residual), PR-close discarding the per-repo IndexJob (design flaw — merge wants indexing), SLO measurement columns missing (created_at), pull_requests.state without closed set + merged_at, /review semantics undefined, pull_request_review_comment consumer undeclared, "review exitosa" excluding partial, api→llm dependency unjustified, disconnect job semantics; micros: summarizer missing from review fase line, analyzer.build without despliegue field.

**Scope (authorized):** Edit `docs/guia_del_proyecto.md` + `docs/mapa_arquitectura.yaml`; manual untouched.

**Tasks:**

- [x] T1 — 9 edits to `guia_del_proyecto.md` (writer: E1-E9) — done, 0 failed anchors
- [x] T2 — 8 edits to `mapa_arquitectura.yaml` (writer: M1-M8 incl. M3b) — done, folded blocks reflowed ≤80 cols, YAML validates
- [x] T3 — Parent readback: markers grepped independently (merged_at ×2, created_at ×3, success o partial guide 1 / mapa 2, prueba/probar conexión both files, sigue su curso both files, identidad del job, summarizer, analyzer.build), yaml.safe_load PASS, 11 `##` sections intact, semantic checks on parsed flujos True — done
- [x] T4 — Pass 6 full re-read of both files post-edit: zero new findings — loop terminated
- [x] T5 — Close: outcome + engram mirror update

**Outcome:** 17 edits across 2 files. IndexJob no longer discarded on PR close (per-repo job; post-merge indexing is the wanted behavior) in 3 places (guide §3.5 cierre, mapa cerrar_pr, mapa vcs reglas); disconnect now explicitly discards ReviewJob/ChatJob + pending IndexJob; SLO §6 measurement chain complete (webhook_deliveries.created_at → comments_sent.created_at); pull_requests.state closed set {open,closed} + merged_at (cycle time F5); /review = re-enqueue with current identity (guide F2 + mapa chatear flujo); both comment events declared as Chat feeders; F4 trigger wording "completada (success o partial)" in 3 places; api→llm justified via "probar conexión" in settings (guide F1 + mapa api nota); summarizer added to review fase line; analyzer.build despliegue field. Cumulative: 5 batches, 99 changes. No git repo — `git init` + ASCII dir rename still recommended (5th flag).

**Verification note:** writer caught a spec error in the parent's check #5 (guide intentionally says "prueba de conexión", mapa "probar conexión" — both grep patterns valid against their own file). Residual greps post-edit: "review exitosa" fully purged, no generic "jobs pendientes del PR" left, UTF-8 clean.

**Route evidence:** Writer trigger fired (2 non-trivial files, 17 discrete edits) — one delegated writer; parent structural readback + pass-6 full re-read (passive docs; no git repo → no commits, no diff-based assess).

---

# Batch 6+7 — Sixth-pass gap closure (2026-09-30, sixth review session, fresh from zero — engram NOT consulted per user instruction)

**Objective:** Fresh full doc review from scratch (no engram reads). Pass 1 found 13 gaps; pass 2 found 4 residual micros; pass 3 found zero — loop terminated.

**Scope (authorized):** Edit `docs/guia_del_proyecto.md` + `docs/mapa_arquitectura.yaml`; manual untouched.

**Tasks:**

- [x] T1 — Writer: 13 edits (E1-E13: invented "1250/h" GitHub figure softened to secondary-rate-limits reality; chat agent role `review` documented; no retroactive review of pre-existing PRs on (re)connect; IndexJob embedding budget cap; single-worker topology assumption explicit; GitHub reopened triggers review; diff-over-limit runs end `partial` (§3.3+§9.6 aligned); Verifier FP publication semantics — FPs unpublished, kept `verified=false`; failed run re-edits summary (no eternal provisional); invitation temp-password delivery without mailer; pull_requests.created_at for cycle time; mapa risk_score ownership + theme contrast-test precision) — done, 13/13 OK, 0 failed anchors
- [x] T2 — Parent readback: 13 grep markers + YAML safe_load PASS + 11 `##` sections intact; writer flagged spec error in parent's check #2 ("secondary rate limits" legitimately pre-existed in §9.7 — count 2 is correct)
- [x] T3 — Pass 2 full re-read: 4 new micros found → batch 7 applied inline by parent (mechanical one-liners, exact anchors): "próxima corrida"→"próximo `IndexJob`" disambiguation; redundant "(sin mailer, …)" trimmed in §3.4; IndexJob embedding-budget pointer added to mapa jobs reglas; F5 step (risk_score + Pre-merge verdict) appended to flujo revisar_pr
- [x] T4 — Pass 3 full re-read: zero new findings — loop terminated (1→2→3 passes: 13→4→0)

**Outcome:** 17 changes across 2 files (13 writer + 4 parent). Cumulative: 6 sessions, 116 changes. Docs now cover: failure-path summary states, budget ownership for every LLM consumer (review/chat/index), review-trigger semantics per VCS action, no-retroactive-review contract, verifier publication rule, credential delivery without mailer, cycle-time inputs. Manual untouched. No git repo — `git init` + ASCII dir rename still pending (6th flag).

**Route evidence:** Batch 6 = writer trigger (2 non-trivial files, 13 edits) → one delegated writer + parent readback; batch 7 = 4 mechanical one-liners inline (trivial-edit exemption; exact-byte anchors, all verified). Passive docs — no review ceremony; no git repo → no work-unit commits, no diff-based assess.

---

# Batch 8 — Eighth-pass gap closure (2026-09-30, eighth review session, fresh from zero — engram NOT consulted per user instruction)

**Objective:** Fresh full doc review from scratch (no engram reads). Pass 1: 11 findings; pass 2: 2 micros; pass 3: 1 (WCAG 1.4.11 accent contrast); pass 4: zero — loop terminated (11→2→1→0).

**External facts verified against primary sources this session:** postgresql.org release notes (PG 18.6 is current minor; PG 19 still Beta 4 — guide claim kept, minor refreshed 18.4→18.6) and api.github.com riverqueue/river (v0.47.0, still 0.x — guide claim kept).

**Findings pass 1 (all applied):**
1. `findings.severity` closed set undeclared → declared `high`/`medium`/`low` (§3.3 new paragraph + §9.8 validates category AND severity + §9.4 linter severity normalization).
2. Guide F3 checklist had no dashboard PR-detail item while mapa `features.prs` is F3 → added (features/prs + DiffViewer, §5.3).
3. `converted_to_draft` job handling undefined → discards pending ReviewJob/ChatJob like closure (§3.5 event filter + draft bullet + mapa vcs reglas).
4. Webhook treatment for disabled repo implicit → explicit: same as unregistered, 2xx + discard (§3.5).
5. "Findings aceptados" metric provenance missing → content-match heuristic vs post-PR commits, no VCS webhook exists for apply (F5).
6. admin/member RBAC boundary undefined → member = triage/read; all settings = admin-only (§3.4 + mapa settings "acciones exclusivas del admin").
7. Secret masking scoped to Gitleaks findings only → publisher masks ALL comments incl. LLM-quoted secrets (§9.4).
8. Master-key rotation job unlisted in inventory → named `RotationJob`, on-demand from dashboard (§9.2 + mapa jobs expone + fase F1).
9. Dashboard CSP absent → restrictive CSP (`default-src 'self'`, no inline scripts) served by API (§9.5).
10. PG minor stale: 18.4 → 18.6 (§2.1).
11. Rootless promise vs systemd unit manager unspecified → Quadlet user units (`systemctl --user`), rootless reaches the analyzer invoked by worker (§3.5 + mapa worker nota).

**Pass 2 micros (parent inline):** repo connect mechanics — registering the repo in settings precedes webhook install (F1 item); `findings.verified` tri-state — null until Verifier runs (F3), then true/false (§3.3 row).

**Pass 3 (parent inline):** `accent` #E8853D fails WCAG 1.4.11 non-text contrast on light theme (≈2.35:1 on crema, ≈2.5:1 on white) → light token → `#C2611A` (≈3.9:1/≈4.2:1), dark unchanged (≈6.7:1); mapa theme contrast test extended to non-text indicators (focus ring).

**Tasks:**

- [x] T1 — Writer: 16 edits (E1-E16), 0 failed anchors, 1 spec error in parent's check #8 caught (pre-existing §5.3 ref never existed)
- [x] T2 — Parent readback: all 16 verified in context, YAML safe_load PASS
- [x] T3 — Pass 2 full re-read: 2 micros applied inline
- [x] T4 — Pass 3 full re-read: 1 finding applied inline
- [x] T5 — Pass 4 full re-read: zero findings — loop terminated

**Outcome:** 19 changes across 2 files (16 writer + 3 parent). Manual untouched. Cumulative: 8 sessions, 135 changes. No git repo — `git init` + ASCII dir rename still pending (7th flag).

**Route evidence:** Batch 8 pass 1 = writer trigger (2 non-trivial files, 16 edits) → one delegated writer + parent readback; passes 2-3 = 3 mechanical edits inline (trivial-edit exemption; exact-byte anchors). Passive docs — no review ceremony; no git repo → no work-unit commits, no diff-based assess.

---

# Batch 9 — Ninth-pass gap closure (2026-09-30, ninth review session, fresh from zero — engram NOT consulted per user instruction)

**Objective:** Fresh full doc review from scratch (no engram reads). Pass 1: 20 findings (5 substantive + 15 minor); pass 2: 2 mechanical residuals from the batch itself; pass 3: zero — loop terminated (20→2→0).

**Scope (authorized):** Edit `guia_del_proyecto.md` + `mapa_arquitectura.yaml`; SCOPE EXTENDED this batch — one scope note added under the title of `manual_completo_de_coderabbit_ai.md` (reference content untouched; the note prevents readers from mistaking the external product's feature list for this project's scope, per the user's "total comprensión del lector" goal).

**Findings pass 1 (all applied):**
1. In-flight review vs PR close: pre-publish re-check only covered (head_sha, base_sha); a running review still published on a closed PR → §3.6.1 + mapa review reglas now abort publication on closed PR (run ends `stale`).
2. LLM roles incomplete + orphan `cheap` role: Summarizer/Verifier/Pre-merge had no role → Summarizer `review`, Verifier `cheap` (mechanical cross-check), Pre-merge `review` (guide §6, F3, F5 + mapa review reglas role line).
3. Deleted-line anchoring undefined: §3.6.5 mapped only RIGHT side → eliminated lines anchor LEFT; out-of-diff → summary (guide + mapa vcs reglas).
4. Analyzer image deploy contradiction: §9.13 "upload image" vs mapa `analyzer.build` (build on VPS) → §9.13 now uploads source + rebuilds on VPS via `analyzer.build`.
5. GitLab self-managed unsupported silently; GHES undeclared → `repositories.base_url` (default gitlab.com) + §1.1 out-of-scope bullet for GHES.
6-20. Minors: `users.must_change_password` flag; password reset revokes sessions; member deactivation (§3.4 + F1); GitLab draft mark = title prefix (`Draft:`/`WIP:`) filter clarification; own payload cap (config) for GitLab; per-call LLM timeout; read-only deploy key; RotationJob drains queue first; SLO P95 visible in dashboard (§9.9); webhook_deliveries retention bounds P95 window (§9.11); reviews→pull_requests FK; SAST findings skip verification (§3.3); mapa api fase split GitHub F1/GitLab F2; manual scope note; analyzer CLI path via argv not stdin.

**Pass 2 residuals (parent inline):** §3.3 `stale` parenthetical updated to "identidad reemplazada — push o retarget — o PR cerrado en vuelo"; mapa role line "index → embedding" → "IndexJob → embedding".

**Tasks:**
- [x] T1 — Writer: 26 edits (E1-E20 + Me1-Me6), 0 failed anchors (3 anchors re-anchored by writer to the file's actual backticked text)
- [x] T2 — Parent readback: all 26 verified in context, YAML safe_load PASS, parsed-mapa semantic checks True (roles/cierre/LEFT/api fase/llm timeout/argv)
- [x] T3 — Pass 2 full re-read: 2 residuals applied inline
- [x] T4 — Pass 3: zero findings — loop terminated

**Outcome:** 28 changes across 3 files. Manual touched for the first time (scope note only). Cumulative: 9 sessions, 163 changes. No git repo — `git init` + ASCII dir rename still pending (8th flag).

**Route evidence:** Batch 9 pass 1 = writer trigger (3 files, 26 edits) → one delegated writer + parent readback; pass 2 = 2 mechanical one-liners inline (trivial-edit exemption). Passive docs — no review ceremony; no git repo → no work-unit commits, no diff-based assess.

---

# Batch 10 — Tenth-pass gap closure (2026-09-30, tenth review session, fresh from zero — engram NOT consulted per user instruction)

**Objective:** Fresh full doc review from scratch (no engram reads). Pass 1: 14 findings (5 substantive + 9 minor); pass 2: 6 residual micros (second-order effects of pass 1); pass 3: 1 micro (§3.1 diagram); pass 4: zero — loop terminated (14→6→1→0).

**Session context:** mid-session the user renamed the repo `revisión-codigo` → `codeowl`, ran `git init`, and committed the writer's pass-1 output themselves (74e75925d701). Parent adapted: verification re-ran against the new path; the guide's §3.2 name alignment was done by the user in parallel (8926b19587df).

**Findings pass 1 (all applied by delegated writer, 25 edits: G1-G14 guide + M1-M11 mapa):**
1. GitLab ready→draft via title edit would enqueue a review of a draft PR (direction filter missing) → filter now passes only draft→ready; the reverse discards pending jobs like `converted_to_draft` (guide §3.5 + mapa vcs reglas).
2. IndexJob: what it clones / when it triggers / how — flujo `indexar_repo` had no clone step, `VCSProvider` contract had no default-branch clone, and "tras un merge, indexar la nueva base" had no merge trigger → `FetchDefaultBranch` added to contract + flujo; merge added as IndexJob trigger (guide §3.5 cierre, F4 + mapa jobs reglas + flujo); index target declared = default branch at run time.
3. Master-key rotation had no key-transition mechanism (naive rotation bricks data) → dual-key `MASTER_KEY` + `MASTER_KEY_PREVIOUS` in api+worker, restart, then RotationJob; refuses to start without the previous key (§9.2 + mapa RotationJob).
4. F5 VCS-read metrics had no computation moment → `MetricsJob` enqueued once at PR close (conversation complete; no LLM) — guide §3.5 cierre + F5, mapa jobs expone/fase + flujo cerrar_pr.
5. F1 queue-status panel deliverable had no structural home → `features/queue` added to guide §3.2 tree + §3.1 diagram (pass 3) + F1 item + mapa features.
6-14. Minors: `loginctl enable-linger` for user units (§3.5 + mapa worker nota); VCS-side installation is manual by the operator (App install / GitLab webhook creation, F1); `/review` on closed PR replies without encola (F2 + mapa chatear flujo); unbalanced paren in mapa FetchPR block (verified +1 by script, fixed); §3.2 root annotation corrected (superseded by user's codeowl rename); `comments_sent` FK a pull_requests declared; member deactivation revokes sessions; "archivos sensibles" = configurable patterns; Verifier tagged F3 in flujo revisar_pr.

**Pass 2 residuals (parent inline, 6 one-liners):** mapa vcs cierre clause updated (MetricsJob enqueue + merge-enqueues-IndexJob); MetricsJob added to repo-disconnect discard list (§3.5); flujo FetchPR "credencial efímera o deploy key"; `FetchPRTimeline` added to VCS contract (MetricsJob input: PR commits + final thread state) + guide F5 pointer; MetricsJob outcome persisted on existing `findings`/`comments_sent` rows (no new tables); metrics heuristics added to §8 unit-test row.

**Pass 3 (parent inline, 1):** §3.1 dashboard diagram now lists Queue.

**Tasks:**
- [x] T1 — Writer: 25 edits (G1-G14, M1-M11), 0 failed anchors, 1 re-anchor on indentation (M9), 1 YAML quoting fix (M10, per config.proposito precedent)
- [x] T2 — Parent readback: all markers grepped independently, YAML safe_load PASS, parens 0/0, 11 `##` sections, §9.2/§3.5/F1-F5/mapa blocks re-read in full context
- [x] T3 — Pass 2 full re-read: 6 micros applied inline
- [x] T4 — Pass 3 full re-read: 1 micro applied inline
- [x] T5 — Pass 4 verification sweep: zero findings — loop terminated
- [x] T6 — Work-unit commit of pass-2/3 residuals (pass-1 output committed by user as 74e75925d701)

**Outcome:** 32 changes this session (25 writer + 7 parent) across 2 files. Cumulative: 10 sessions, 195 changes. Manual untouched. Git repo now exists (`codeowl`, main) — rename + `git init` done by the user mid-session (resolved the 9-session-old recommendation).

**Verification:** `yaml.safe_load` PASS; paren balance 0/0 both files; `revision-codigo` remnants 0; grep markers all present (MetricsJob guia 3 / mapa 5, FetchDefaultBranch 3+1, FetchPRTimeline 1+1, queue feature/diagram, linger, dual-key, draft direction, FK, heurísticas).

**Route evidence:** Batch 10 pass 1 = writer trigger (2 non-trivial files, 25 edits) → one delegated writer + parent readback; passes 2-3 = 7 mechanical one-liners inline (trivial-edit exemption; exact-byte anchors). Passive docs — no review ceremony; git repo now exists → residual edits closed as a work-unit commit.

---

# Batch 11 — Eleventh-pass gap closure (2026-10-01, eleventh review session, fresh from zero — engram NOT consulted per user instruction)

**Objective:** Fresh full doc review from scratch (no engram reads). Pass 1: 10 findings (1 verified against GitLab primary docs via webfetch); pass 2: 1 micro (mapa `actualizado` date); pass 3: zero — loop terminated (10→1→0).

**External facts verified against primary sources this session:** docs.gitlab.com webhooks — GitLab 19+ ships signing tokens (HMAC-SHA256, Standard Webhooks: `webhook-signature` over `webhook-id.webhook-timestamp.body`, constant-time compare, timestamp freshness vs replay; `webhook-id` on every delivery); plain `X-Gitlab-Token` not recommended for new webhooks → guide §9.3 was documenting the weak deprecated mechanism.

**Findings pass 1 (all applied by delegated writer, 17 ops: A1-A14 guide + B1-B3 mapa + README):**
1. §9.3 verification outdated → GitHub (`X-Hub-Signature-256` + `X-GitHub-Delivery`) and GitLab 19+ (signing token, Standard Webhooks, replay freshness; secret-token fallback for old self-managed) both specified; delivery-ID sources named in §9.3 + §3.3 `webhook_deliveries`.
2. Analyzer read-only rootfs vs compilers needing writable space → caches/artifacts to workdir or size-capped tmpfs (config) (§9.4 + mapa analyzer reglas).
3. §9.12 "no duplica comentarios" claim was optimistic → publisher reconciles comments_sent + bot's live comments before retrying inline posts.
4. Natural keys for idempotent upsert: pull_requests (repo + número/iid), repositories.external_id = numeric VCS repo id.
5. GitLab connect mechanics missing deploy-key operator step → F1 item now includes it.
6. GitLab bot detection: no bot flag in payload → match bot username (config) (§3.5 + mapa vcs reglas).
7. No README at repo root → README.md created (entry point + doc index + governance) + §3.2 tree entry.
8. Partial coverage undisclosed in PR → fase-2 re-edit declares partial + reason (§9.6 + mapa llm reglas).
9. llm_providers priority tie-break → by id, stable order.
10. Cycle time definition → creación → merge (§3.3).

**Pass 2 micro (parent inline):** mapa `actualizado` 2026-09-30 → 2026-10-01.

**Tasks:**

- [x] T1 — Writer: 17 ops, 0 failed anchors, skill cognitive-doc-design loaded (paths-injected)
- [x] T2 — Parent readback: 15 semantic checks PASS, YAML safe_load PASS, parens 0/0 both files, 11 `##` sections intact (unbalanced `[` in guia = pre-existing §3.1 ASCII art, benign in code block)
- [x] T3 — Pass 2 full re-read: 1 micro applied inline
- [x] T4 — Pass 3 verification sweep: zero findings — loop terminated
- [x] T5 — Work-unit commit b72fa7f7633c + RDD assess: medium (config-file heuristic on the YAML), review_due=false (under_budget, 54 lines) — stays pending in slice

**Outcome:** 18 changes across 3 files (17 writer + 1 parent). Manual untouched (scope note intact). Cumulative: 11 sessions, 213 changes.

**Route evidence:** Batch 11 pass 1 = writer trigger (3 non-trivial files, 17 ops) → one delegated writer + parent readback; pass 2 = 1 mechanical one-liner inline (trivial-edit exemption). Passive docs; work-unit commit on main (repo precedent for docs batches); assess run post-commit with explicit untracked inventory (.atl excluded).


---

# Batch 12 — Twelfth-pass gap closure (2026-10-01, twelfth review session, fresh from zero — engram NOT consulted per user instruction)

**Objective:** Fresh full doc review from scratch (no engram reads). Pass 1: 10 findings; pass 2: zero (second-order effects of pass-1 edits all closed clean); pass 3: zero — loop terminated (10→0→0).

**External facts verified against primary sources this session (web, 2026-10-01):** Go 1.27.1 current (go.dev) ✓ · PostgreSQL 18.6 stable + PG 19 Beta 4 (postgresql.org via Wikipedia infobox + press-release title) ✓ · Tailwind v4.3.3 (GitHub tags) ✓ · River v0.48.0 — still 0.x as the guide claims (GitHub releases) ✓ · `pgvector/pgvector:pg18` tag exists, 0.8.6 (Docker Hub) ✓ · GitLab signing token: introduced 19.0 behind FF `webhook_signing_token`, **GA 19.1** (docs.gitlab.com) — guide's "19+" imprecise + verification mechanics incomplete · **Podman current is v6.1.3, repo moved to podman-container-tools/podman** — guide said "Podman 5" (stale).

**Findings pass 1 (10, all applied by delegated writer; 9 guide G1-G8 incl. one two-part + 1 mapa):**
1. §2 stack table "Podman 5" stale → Podman 6 (web-verified v6.1.3, Sept 2026).
2. §9.3 GitLab signing-token verification lacked implementable mechanics → exact recipe added: token prefix `whsec_` (strip + base64-decode = raw HMAC key), `webhook-signature` carries one-or-more `v1,{base64}` signatures space-separated, constant-time compare against each, timestamp freshness; "19+" → "GA desde 19.1".
3. §3.3 `reviews.partial` enumerated only 2 causes but §9.6/§9.7 define 3 → "failover agotado" added (§9.6-§9.7 ref).
4. Mapa jobs assigns `CleanupJob`+`RotationJob` to F1 but guide F1 checklist had no item → "Jobs de operación" bullet added (retención §9.11; rotación §9.2 acción settings/admin).
5. Mapa `lib` fase F0 while `usePR` (PR detail) is F3 → "F0 (apiClient, useSession) / F3 (usePR)".
6. Chat on closed PR undefined outside `/review` → F2 bullet: demás comandos responden igual, el hilo sigue vivo, solo `/review` se rehúsa.
7. No guard against enabling a repo with zero `review`-role providers → §9.6: conectar/re-habilitar exige proveedor enabled en rol `review`; settings bloquea (each PR would burn a full retry run to `failed`).
8. §3.5 "Eventos suscritos" was a single ~2.6 KB bullet covering 6 topics → parent + 6 nested sub-bullets (GitHub/GitLab events, early discard, repo ausente-desconectado, pre-conexión, filtros de comentario), text verbatim.
9. SPA deep-link 404 gap → §3.5: API serves statics with SPA fallback (non-`/api`/`/webhooks` client routes → `index.html`).
10. F1 had two redundant GitHub App bullets → merged (webhook/tokens/idempotencia + secrets en env + permisos mínimos in one).

**Pass 2 (parent):** zero new findings — re-read changed regions + second-order checks (no other "Podman 5"/"19+" remnants; RotationJob trigger consistent with §9.2 settings action; guard consistent with mapa settings scope).

**Pass 3 (parent):** structural sweep — parens 0/0 both files (1 stray `[` = pre-existing §3.1 ASCII art, benign), YAML safe_load PASS, 11 `##` sections, 12/12 writer verifications re-confirmed.

**Tasks:**
- [x] T1 — Writer: 10 edits, 0 failed anchors, skill cognitive-doc-design loaded
- [x] T2 — Parent readback: §3.5 restructure, §9.3, F1/F2 bullets, §3.3 row verified in full context post-edit
- [x] T3 — Pass 2 full re-read: zero findings
- [x] T4 — Pass 3 verification sweep: zero findings — loop terminated
- [x] T5 — Housekeeping: batch-11 odd record (left uncommitted by prior session) committed as 4e7e41b54b51, then this batch's record + docs as one work-unit commit

**Outcome:** 10 changes across 2 files (manual + README untouched — no findings there this pass). Cumulative: 12 sessions, 223 changes.

**Route evidence:** Batch 12 pass 1 = writer trigger (2 non-trivial files, 10 edits) → one delegated writer + parent readback; passes 2-3 = read-only (nothing to apply). Passive docs — no review ceremony; work-unit commits on main (repo precedent for docs batches); RDD assess run post-commit (base-ref b72fa7f7633c, committed-only).

---

# Batch 13 — Thirteenth-pass gap closure (2026-10-01, thirteenth review session, fresh from zero — engram NOT consulted per user instruction)

**Objective:** Fresh full doc review from scratch (no engram reads). Pass 1: 4 findings (2 substantive + 2 consistency micros); pass 2: 2 residuals (second-order effects of the pass-1 per-file-cap edit); pass 3: zero — loop terminated (4→2→0).

**External facts verified against primary sources this session (web, 2026-10-01):** go.dev/dl — Go 1.27.1 stable ✓ (guide "Go 1.27" current) · api.github.com riverqueue/river — v0.48.0 released today, still 0.x ✓ (guide claim holds) · postgresql.org — 18.6 latest stable (2026-08-13), PG 19 Beta 4 (2026-09-24) ✓ (guide claim exact). Podman 6 / Tailwind 4.3 / pgvector pg18 unchanged from batch-12 verification (same day).

**Findings pass 1 (4, all applied by delegated writer, 5 edits: E1-E3 guide + E4-E5 mapa):**
1. Roles `cheap`/`embedding` without an enabled provider: behavior undefined (§9.6 guard covered only `review`) → they degrade instead of block: no `cheap` → Verifier doesn't run, findings publish with `verified` null (§3.3), fase-2 re-edit declares it; no `embedding` → IndexJob not enqueued (structured log), indexing dormant — enqueueing it would burn retries on every completed review (§9.6 + mapa review reglas + mapa jobs reglas).
2. Per-file size cap missing: a PR under the PR-level diff limit can carry one pathological file (context overflow + retry-budget burn) → per-file cap (config): file over cap is SAST-only, corrida ends `partial`, declared in the fase-2 coverage note (§9.6).
3. Mapa role line omitted `Chat → review` (guide F2 declares it) → added, with per-role degradation clauses (mapa review reglas).
4. §3.3 `comments_sent`: dedup huella attributed to every row, but it only applies to `inline` rows → "solo filas `inline`" scoping added.

**Pass 2 residuals (parent inline, 2):** the new per-file cap is a 4th `partial` cause → §9.6 motivo list now "presupuesto agotado, diff o archivo sobre el tope, o failover agotado"; §3.3 `reviews` partial enumeration now includes "archivo sobre el tope por archivo".

**Tasks:**

- [x] T1 — Writer: 5 edits, 0 failed anchors, skill cognitive-doc-design loaded (paths-injected); writer caught spec error in parent's check #1 ("Sin proveedor" capitalized at sentence start → case-sensitive grep; content correct)
- [x] T2 — Parent readback: all 5 edits verified in full context (E1/E3 §9.6 flow reads clean, E4/E5 folded blocks ≤80 cols, 6-space indent preserved), YAML safe_load PASS
- [x] T3 — Pass 2 full re-read: 2 residuals applied inline; F3/F4 checked against the new degradation rules — no contradiction
- [x] T4 — Pass 3 structural sweep: YAML OK, parens 0/0 both files (bracket 1 = pre-existing §3.1 ASCII art, benign), 11 `##` sections, diff = exactly the 7 intended changes — loop terminated
- [x] T5 — Work-unit commit 953fad2b01aa + RDD assess: medium (config-file heuristic on the YAML), review_due=false (under_budget, 43 lines) — stays pending in slice; untracked `.atl/` excluded with explicit inventory declaration

**Outcome:** 7 changes across 2 files (5 writer + 2 parent). Manual + README untouched. Cumulative: 13 sessions, 230 changes.

**Route evidence:** Batch 13 pass 1 = writer trigger (2 non-trivial files, 5 edits) → one delegated writer + parent readback; pass 2 = 2 mechanical one-liners inline (trivial-edit exemption; exact-byte anchors). Passive docs — no review ceremony; work-unit commit on main (repo precedent); RDD assess post-commit.

---

# Batch 14 — Fourteenth-pass gap closure (2026-10-01, fourteenth review session, fresh from zero — engram NOT consulted per user instruction)

**Objective:** Fresh full doc review from scratch (no engram reads) with web knowledge refresh. Pass 1: 14 findings (all applied); iteration continues until a clean pass.

**External facts verified against primary sources this session (web, 2026-10-01):** go.dev — Go 1.27.0 (2026-08-19) current ✓ · postgresql.org — PG 18.6 current minor, PG 19 Beta 4 (2026-09-24) ✓ exact · riverqueue/river releases — v0.48.0, still 0.x ✓ · containers/podman releases — v6.1.3, major 6 ✓ · tailwindcss.com/blog — v4.3 latest ✓ · docs.gitlab.com webhooks — signing token GA 19.1, Standard Webhooks mechanics (whsec_ strip + b64, v1,{b64} space-separated, id.timestamp.body, constant-time, timestamp freshness) exact match with §9.3 ✓ · docs.gitlab.com drafts — prefixes [Draft]/Draft:/(Draft), WIP gone ✓ · hub.docker.com — pgvector/pgvector:pg18 tag exists (0.8.6) ✓ · docs.coderabbit.ai/reference/configuration (updated 2026-09-30) — `version` field REMOVED from schema (manual sample stale), profile set = quiet/chill/assertive (manual comment said assertive/chill/strict — stale) ✗→fixed.

**WCAG numeric audit of §5.1 (computed, not trusted):** all TEXT pairs pass AA exactly as documented (12.67:1, 14.12:1, 6.30:1, 8.25:1, muted 5.54/9.14, severities 6.09-10.26). `border.subtle` = 1.93:1 claro / 1.76:1 oscuro — below WCAG 1.4.11 non-text 3:1 while §5.2 claimed "cada token ya cumple" ✗→fixed (decorative-only exception documented).

**Findings pass 1 (14, all applied: 11 guide via delegated writer E1-E11 + 3 manual E12-E14):**
1. §5.2 "cada token ya cumple" vs border.subtle <3:1 → rule 1 now scopes to text tokens + explicit decorative exception (dividers; interactive components identify via label/placeholder/text.muted, focus ring for active state).
2. §3.4 "preserva el histórico de sus acciones" — no audit table backs it → reworded to what flag-not-delete actually preserves; "sin bitácora de auditoría por usuario" made explicit (YAGNI single-org).
3. §9.3 processing order unpinned → cheap-first order appended (tamaño → firma → dedup → parseo → filtro; nothing touches queue/BD before signature passes).
4. §3.5 chat on draft PRs unspecified → chat operative on drafts (own budget §9.6), `/review` refuses while draft (same refusal class as closed PR, F2).
5. §3.3 `llm_usage.job_id` nullability for the settings connection-probe call → documented inline.
6. §9.6 = single 3052-char paragraph, ~10 limits → restructured to lead line + 7 grouped bullets, zero content loss (12-clause audit passed).
7. §3.2 tree missing `.github/workflows/` (F0 CI) → added.
8. §3.2 justfile comment missing `deploy` (used by §9.13) → added with §9.13 ref.
9. §3.2 tree missing `odd/` (real repo dir) → added as process-docs line.
10. §3.2 config label "validación al arranque" contradicted mapa's staged validation → aligned (essentials at boot, VCS creds on connect).
11. §2.1 date septiembre→octubre 2026 (all claims re-verified current this session).
12. Guide ToC added (11 anchor links, GitHub slug algorithm with accents/em-dash).
13. Manual: stale `version: "2"` removed from .coderabbit.yaml sample (field gone from schema).
14. Manual: profile comment corrected to quiet/chill/assertive + scope note gains freshness caveat (verified 2026-10, not kept in sync).

**Open (user decision, NOT written):** repo license — no LICENSE file, README silent; product decision pending.

**Tasks:**

- [x] T1 — Writer: 14 edits applied, 0 failed anchors, 0 deviations; writer verified §9.6 content-loss audit (12 clauses) + all greps green; writer flagged 2 observations (mapa uncommitted residual → reconciled as batch-13 leftover commit 90345984cdd8; guide F3 profile set divergence → deferred to pass 2)
- [x] T2 — Parent readback: ToC + §9.6 verified in full; all 11 grep markers independently confirmed
- [x] T3 — Reconciled uncommitted mapa residual (--memory, ChatJob idempotency anchor — mirrors committed guide §9.4/§3.3) → separate commit 90345984cdd8 before batch 14 files
- [x] T4 — Work-unit commit batch 14 files
- [x] T5 — RDD assess post-commit; iterate pass 2 fresh from zero

**Outcome:** 14 changes across 2 files (guide 11, manual 3) + 1 reconciliation commit. Cumulative: 14 sessions, 244+ changes.

**Route evidence:** Batch 14 pass 1 = writer trigger (2 non-trivial files, 14 edits) → one delegated writer with exact-byte edit spec + parent readback. Passive docs — no review ceremony; work-unit commits on main (repo precedent).
