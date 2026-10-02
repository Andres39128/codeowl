/** useSession sobre apiClient mockeado: sesión, 401 y login/logout. */

import { renderHook, waitFor } from "@testing-library/preact";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createWrapper } from "../test/createWrapper";
import { ApiError, getCsrfToken, setCsrfToken } from "./apiClient";
import { type User, useSession } from "./useSession";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));

vi.mock("./apiClient", async (importOriginal) => {
	const actual = await importOriginal<typeof import("./apiClient")>();
	return { ...actual, api: apiMock };
});

const user: User = {
	username: "admin",
	role: "admin",
	must_change_password: false,
};

beforeEach(() => {
	apiMock.mockReset();
	setCsrfToken(null);
});

describe("useSession", () => {
	it("carga la sesión y guarda el CSRF en memoria", async () => {
		apiMock.mockResolvedValueOnce({ user, csrf_token: "csrf-1" });

		const { result } = renderHook(() => useSession(), {
			wrapper: createWrapper(),
		});

		await waitFor(() => expect(result.current.user).toEqual(user));
		expect(getCsrfToken()).toBe("csrf-1");
		expect(apiMock).toHaveBeenCalledWith("/api/auth/session");
	});

	it("401 → sin sesión y sin error (estado previo al login)", async () => {
		apiMock.mockRejectedValueOnce(new ApiError(401, "credenciales inválidas"));

		const { result } = renderHook(() => useSession(), {
			wrapper: createWrapper(),
		});

		await waitFor(() => expect(result.current.isLoading).toBe(false));
		expect(result.current.user).toBeNull();
		expect(result.current.error).toBeNull();
	});

	it("login guarda el CSRF y publica el usuario en la cache", async () => {
		apiMock.mockResolvedValueOnce(null); // sin sesión previa
		const { result } = renderHook(() => useSession(), {
			wrapper: createWrapper(),
		});
		await waitFor(() => expect(result.current.isLoading).toBe(false));

		apiMock.mockResolvedValueOnce({ user, csrf_token: "csrf-2" });
		result.current.login.mutate({ username: "admin", password: "secreto" });

		await waitFor(() => expect(result.current.user).toEqual(user));
		expect(getCsrfToken()).toBe("csrf-2");
		expect(apiMock).toHaveBeenCalledWith("/api/auth/login", {
			method: "POST",
			body: { username: "admin", password: "secreto" },
		});
	});

	it("logout limpia el CSRF y la sesión", async () => {
		apiMock.mockResolvedValueOnce({ user, csrf_token: "csrf-3" });
		const { result } = renderHook(() => useSession(), {
			wrapper: createWrapper(),
		});
		await waitFor(() => expect(result.current.user).toEqual(user));

		apiMock.mockResolvedValueOnce({ ok: true });
		result.current.logout.mutate();

		await waitFor(() => expect(result.current.user).toBeNull());
		expect(getCsrfToken()).toBeNull();
		expect(apiMock).toHaveBeenCalledWith("/api/auth/logout", {
			method: "POST",
		});
	});
});
