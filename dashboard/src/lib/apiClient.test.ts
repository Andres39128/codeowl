/** Contrato del apiClient: CSRF en mutantes, credentials, errores tipados. */

import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api, getCsrfToken, setCsrfToken } from "./apiClient";

const fetchMock = vi.fn();
vi.stubGlobal("fetch", fetchMock);

afterEach(() => {
	fetchMock.mockReset();
	setCsrfToken(null);
});

function jsonResponse(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), {
		status,
		headers: { "Content-Type": "application/json" },
	});
}

describe("api", () => {
	it("GET: sin CSRF, credentials include y parsea el JSON", async () => {
		fetchMock.mockResolvedValueOnce(jsonResponse(200, { user: null }));
		const data = await api<{ user: unknown }>("/api/auth/session");

		expect(data).toEqual({ user: null });
		const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
		expect(init.credentials).toBe("include");
		expect(
			(init.headers as Record<string, string>)["X-CSRF-Token"],
		).toBeUndefined();
	});

	it("POST: adjunta X-CSRF-Token y serializa el cuerpo", async () => {
		setCsrfToken("csrf-123");
		fetchMock.mockResolvedValueOnce(jsonResponse(200, { ok: true }));

		await api("/api/auth/login", {
			method: "POST",
			body: { username: "admin" },
		});

		const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
		expect((init.headers as Record<string, string>)["X-CSRF-Token"]).toBe(
			"csrf-123",
		);
		expect((init.headers as Record<string, string>)["Content-Type"]).toBe(
			"application/json",
		);
		expect(init.body).toBe(JSON.stringify({ username: "admin" }));
	});

	it("GET nunca adjunta CSRF aunque haya token", async () => {
		setCsrfToken("csrf-123");
		fetchMock.mockResolvedValueOnce(jsonResponse(200, {}));

		await api("/healthz");

		const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
		expect(init.method).toBe("GET");
		expect(
			(init.headers as Record<string, string>)["X-CSRF-Token"],
		).toBeUndefined();
	});

	it("error no-ok: ApiError con status y mensaje del cuerpo", async () => {
		fetchMock.mockResolvedValueOnce(
			jsonResponse(401, { error: "credenciales inválidas" }),
		);

		const error = await api("/api/auth/login", {
			method: "POST",
			body: {},
		}).catch((caught: unknown) => caught);

		expect(error).toBeInstanceOf(ApiError);
		expect((error as ApiError).status).toBe(401);
		expect((error as ApiError).message).toBe("credenciales inválidas");
	});

	it("error sin JSON válido: mensaje por status", async () => {
		fetchMock.mockResolvedValueOnce(
			new Response("gateway timeout", { status: 504 }),
		);

		const error = await api("/healthz").catch((caught: unknown) => caught);

		expect((error as ApiError).status).toBe(504);
		expect((error as ApiError).message).toBe("HTTP 504");
	});

	it("getCsrfToken refleja el token en memoria", () => {
		expect(getCsrfToken()).toBeNull();
		setCsrfToken("abc");
		expect(getCsrfToken()).toBe("abc");
	});
});
