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
