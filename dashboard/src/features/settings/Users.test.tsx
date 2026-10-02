/** Users: invitación con contraseña temporal de una sola vista y protección
 * de auto-bloqueo (mapa: dashboard.features.settings). */

import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
	within,
} from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/apiClient";
import type { ManagedUser } from "../../lib/types";
import { createWrapper } from "../../test/createWrapper";
import { Users } from "./Users";

vi.mock("../../lib/apiClient", () => ({
	ApiError: class ApiError extends Error {
		readonly status: number;
		constructor(status: number, message: string) {
			super(message);
			this.name = "ApiError";
			this.status = status;
		}
	},
	api: vi.fn(),
	setCsrfToken: vi.fn(),
}));

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const users: ManagedUser[] = [
	{
		id: 1,
		username: "root",
		role: "admin",
		must_change_password: false,
		disabled: false,
	},
	{
		id: 2,
		username: "ana",
		role: "member",
		must_change_password: true,
		disabled: false,
	},
];

function mockBackend() {
	mockedApi.mockImplementation((path: string, opts?: { method?: string }) => {
		if (path === "/api/auth/session") {
			return Promise.resolve({
				user: {
					username: "root",
					role: "admin",
					must_change_password: false,
				},
				csrf_token: "token-test",
			});
		}
		if (path === "/api/users" && opts?.method === "POST") {
			return Promise.resolve({
				username: "nuevo",
				temp_password: "abcd1234efgh5678",
			});
		}
		if (path.startsWith("/api/users/")) {
			return Promise.resolve({
				temp_password: "nueva0987654321ab",
			});
		}
		if (path === "/api/users") return Promise.resolve(users);
		return Promise.reject(new Error(`ruta inesperada: ${path}`));
	});
}

describe("Users", () => {
	beforeEach(() => {
		mockedApi.mockReset();
		mockBackend();
	});

	afterEach(cleanup);

	it("invita un miembro y muestra la contraseña temporal UNA vez", async () => {
		render(<Users />, { wrapper: createWrapper() });
		await screen.findByText("ana");

		fireEvent.click(screen.getByText("Invitar miembro"));
		fireEvent.input(screen.getByLabelText("Usuario"), {
			target: { value: "nuevo" },
		});
		fireEvent.click(screen.getByRole("button", { name: "Invitar" }));

		expect(await screen.findByText(/abcd1234efgh5678/)).toBeTruthy();
		expect(screen.getByText(/una sola vez/)).toBeTruthy();
		await waitFor(() => {
			expect(mockedApi).toHaveBeenCalledWith("/api/users", {
				method: "POST",
				body: { username: "nuevo" },
			});
		});
	});

	it("no permite deshabilitarse a sí mismo (oculta el botón)", async () => {
		render(<Users />, { wrapper: createWrapper() });
		await screen.findByText("root");

		// "ana" (member) tiene su botón; "root" (la sesión actual) no.
		expect(screen.getByText("Deshabilitar")).toBeTruthy();
		const rootRow = screen.getByText("root").closest("tr");
		expect(rootRow?.textContent).not.toContain("Deshabilitar");
	});

	it("reset de contraseña muestra la nueva temporal en el banner", async () => {
		render(<Users />, { wrapper: createWrapper() });
		await screen.findByText("ana");

		// El reset se scopea a la fila de ana (id 2), no a la primera hallada.
		const anaRow = screen.getByText("ana").closest("tr");
		fireEvent.click(
			within(anaRow as HTMLElement).getByText("Resetear contraseña"),
		);

		expect(await screen.findByText(/nueva0987654321ab/)).toBeTruthy();
		await waitFor(() => {
			expect(mockedApi).toHaveBeenCalledWith("/api/users/2", {
				method: "PUT",
				body: { action: "reset_password" },
			});
		});
	});
});
