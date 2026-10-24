/** Tipos de las respuestas de la API en las fronteras del dashboard (§4.6).
 * Campos snake_case = contrato JSON del backend (internal/api). */

/** Proveedor LLM: la api_key viaja enmascarada ("sk-***xyz"), jamás en claro (§9.9). */
export interface Provider {
	id: number;
	base_url: string;
	model: string;
	api_key: string;
	role: "review" | "cheap" | "embedding";
	priority: number;
	enabled: boolean;
}

/** Repo conectado: sin secretos, ni siquiera enmascarados (actualizar = re-enviar, §3.5). */
export interface Repo {
	id: number;
	vcs: "github" | "gitlab";
	external_id: number;
	owner: string;
	name: string;
	enabled: boolean;
	review_drafts: boolean;
	language: string;
	chat_org_only: boolean;
}

/** Usuario listado en settings: jamás el hash de contraseña (§3.4). */
export interface ManagedUser {
	id: number;
	username: string;
	role: "admin" | "member";
	must_change_password: boolean;
	disabled: boolean;
}

/** Salud de un repo (GET /api/repos/{id}/health): los checks que explican
 * "¿por qué no se revisó mi PR?". app_installed null = no aplica (GitLab)
 * o no se pudo verificar. */
export interface RepoHealth {
	app_installed: boolean | null;
	last_delivery_at: string | null;
	has_review_provider: boolean;
	open_prs: number;
	last_review_at: string | null;
	latest_open_pr: { number: number; head_sha: string; base_sha: string } | null;
}

/** Fila de la historia de jobs (GET /api/jobs/history): un job finalizado con
 * duración de ejecución y el último error si lo hubo. */
export interface JobHistoryRow {
	kind: string;
	state: "completed" | "cancelled" | "discarded";
	created_at: string;
	finalized_at: string;
	duration_ms: number;
	error: string | null;
}

/** Fila del panel de cola (§9.9): river_job agrupado por (kind, state). */
export interface JobRow {
	kind: string;
	state: "available" | "pending" | "running" | "discarded";
	count: number;
	latest_created_at: string;
}

/** Estados de una corrida de revisión (conjunto cerrado del schema §3.3). */
export type ReviewStatus =
	| "running"
	| "success"
	| "partial"
	| "stale"
	| "failed";

/** Repo embebido de un PR (internal/api/prs.go): lo mínimo para el origen. */
export interface PrRepoView {
	id: number;
	owner: string;
	name: string;
	vcs: string;
}

/** Hallazgos de la última corrida por severidad (high/medium/low, §3.3). */
export interface ReviewCounts {
	high: number;
	medium: number;
	low: number;
}

/** Corrida más reciente del PR, versión lista (GET /api/prs). */
export interface LatestReviewView {
	id: number;
	status: ReviewStatus;
	created_at: string;
	counts: ReviewCounts;
}

/** PR en el listado y bloque "pr" del detalle (internal/api/prs.go: prView). */
export interface PrView {
	id: number;
	number: number;
	author: string;
	state: "open" | "closed";
	repo: PrRepoView;
	head_sha: string;
	base_ref: string;
	updated_at: string;
	/** Score 0-100 de la última corrida (F5): null si aún no corrió ninguna. */
	risk_score: number | null;
	/** Triage extendido (F6): esfuerzo 0-100 — siempre computable del diff. */
	effort_score: number | null;
	/** Impacto 0-100 (reachability inversa del grafo de imports): null sin
	 * índice del repo — degrada visible. */
	impact_score: number | null;
	latest_review: LatestReviewView | null;
}

/** Corrida completa del detalle: textos que completó la corrida (§3.3). */
export interface ReviewView {
	id: number;
	status: ReviewStatus;
	summary: string;
	walkthrough: string;
	mermaid: string;
	created_at: string;
}

/** Hallazgo del detalle: suggestion y verified opcionales por diseño (§3.3,
 * F3 — verified queda null hasta que corre el Verifier; los SAST no aplican). */
export interface FindingView {
	id: number;
	/** Corrida que produjo el hallazgo: el detalle las agrupa (F6 — la
	 * atribución por corrida ya vive en el modelo, §3.3). */
	review_id: number;
	file: string;
	line: number;
	severity: "high" | "medium" | "low";
	category: string;
	body: string;
	suggestion: string | null;
	/** Accepted: el metrics job (§6 F5) evaluó si la sugerencia terminó
	 * aplicada. null = sin evaluar (el PR sigue abierto). */
	accepted: boolean | null;
	/** GithubCommentID: id del comentario que el hallazgo produjo en GitHub.
	 * Con él se permalinkea al comentario real. */
	github_comment_id: string | null;
	source: "llm" | "sast";
	verified: boolean | null;
}

/** Cohorte de dependencia del change stack (F6 §6): nivel del orden
 * topológico por imports del repo revisado — 0 es la base (lo primero que
 * hay que revisar); kind "tests" va SIEMPRE al final (heurística de ruta). */
export interface StackCohort {
	level: number;
	kind: "code" | "tests";
	files: string[];
}

/** Cuerpo de GET /api/prs/{id}: PR + última corrida + hallazgos. */
export interface PrDetailView {
	pr: PrView;
	review: ReviewView | null;
	findings: FindingView[];
	/** Cohortes de dependencia del diff (F6): ausente o null sin índice del
	 * repo — degradación visible, la Card no se renderiza. */
	change_stack?: StackCohort[] | null;
}

/** Cuerpo de GET /api/prs/{id}/diff (proxy al adapter vía GetDiff, F3). */
export interface PrDiffView {
	diff: string;
}

/** Cuerpo de GET /api/metrics (F5): agregados de outcome y costo LLM de los
 * últimos N días. Las tasas viajan 0..1 o null cuando el denominador es 0 —
 * null honesto, no 0% disfrazado de dato (decisión 8). */
export interface MetricsView {
	window_days: number;
	merged_prs: number;
	avg_cycle_time_hours: number | null;
	findings_with_outcome: number;
	accepted: number;
	accepted_rate: number | null;
	resolved_comments: number;
	false_positives: number;
	false_positive_rate: number | null;
	reviews_with_cost: number;
	avg_tokens_per_review: number | null;
}
