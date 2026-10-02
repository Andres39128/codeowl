/** Cliente API del dashboard: fetch tipado con cookie de sesión y CSRF (§3.4). */

// Base relativa: en dev el proxy de Vite reenvía /api a localhost:8080; en
// prod la API sirve los estáticos con el mismo origen (guía §3.5). No hay CORS.
const BASE_URL = "";

/**
 * Token CSRF de la sesión, guardado solo en memoria (guía §3.4): llega en el
 * cuerpo de login y de GET /api/auth/session, que el shell consulta a cada
 * carga — sessionStorage no agrega nada y sobra. Lo consumen las mutaciones.
 */
let csrfToken: string | null = null;

export function setCsrfToken(token: string | null): void {
	csrfToken = token;
}

export function getCsrfToken(): string | null {
	return csrfToken;
}

/** Error tipado de la API: status HTTP + mensaje del cuerpo {"error": "..."}. */
export class ApiError extends Error {
	readonly status: number;

	constructor(status: number, message: string) {
		super(message);
		this.name = "ApiError";
		this.status = status;
	}
}

interface RequestOptions {
	method?: string;
	body?: unknown;
}

/** GET por defecto; JSON in/out; adjunta X-CSRF-Token en todo mutante. */
export async function api<T>(
	path: string,
	options: RequestOptions = {},
): Promise<T> {
	const method = options.method ?? "GET";
	const headers: Record<string, string> = {};
	if (options.body !== undefined) {
		headers["Content-Type"] = "application/json";
	}
	if (method !== "GET" && csrfToken) {
		headers["X-CSRF-Token"] = csrfToken;
	}

	const response = await fetch(BASE_URL + path, {
		method,
		headers,
		credentials: "include",
		body: options.body === undefined ? undefined : JSON.stringify(options.body),
	});

	if (!response.ok) {
		let message = `HTTP ${response.status}`;
		try {
			const data: unknown = await response.json();
			if (data !== null && typeof data === "object" && "error" in data) {
				const err = (data as { error: unknown }).error;
				if (typeof err === "string") message = err;
			}
		} catch {
			// cuerpo sin JSON válido: queda el mensaje por status
		}
		throw new ApiError(response.status, message);
	}

	return (await response.json()) as T;
}
