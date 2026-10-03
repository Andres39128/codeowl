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
	file: string;
	line: number;
	severity: "high" | "medium" | "low";
	category: string;
	body: string;
	suggestion: string | null;
	source: "llm" | "sast";
	verified: boolean | null;
}

/** Cuerpo de GET /api/prs/{id}: PR + última corrida + hallazgos. */
export interface PrDetailView {
	pr: PrView;
	review: ReviewView | null;
	findings: FindingView[];
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
