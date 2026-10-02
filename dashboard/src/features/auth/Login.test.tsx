/** Login (mapa: dashboard.features — vitest por feature) con apiClient mockeado. */

import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/apiClient";
import { createWrapper } from "../../test/createWrapper";
import { Login } from "./Login";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));

vi.mock("../../lib/apiClient", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../lib/apiClient")>();
	return { ...actual, api: apiMock };
});

function renderLogin() {
	const Wrapper = createWrapper();
	return render(
		<Wrapper>
			<Login />
		</Wrapper>,
	);
}

function fillAndSubmit(username: string, password: string) {
	fireEvent.input(screen.getByLabelText("Usuario"), {
		target: { value: username },
	});
	fireEvent.input(screen.getByLabelText("Contraseña"), {
		target: { value: password },
	});
	fireEvent.click(screen.getByRole("button", { name: /iniciar sesión/i }));
}

beforeEach(() => {
	apiMock.mockReset();
});

afterEach(cleanup);

describe("Login", () => {
	it("envía las credenciales al endpoint de login", async () => {
		apiMock.mockResolvedValue({ user: {}, csrf_token: "csrf" });
		renderLogin();

		fillAndSubmit("andres", "secreto");

		await waitFor(() =>
			expect(apiMock).toHaveBeenCalledWith("/api/auth/login", {
				method: "POST",
				body: { username: "andres", password: "secreto" },
			}),
		);
	});

	it("muestra el error del backend cuando el login falla", async () => {
		apiMock.mockRejectedValue(new ApiError(401, "credenciales inválidas"));
		renderLogin();

		fillAndSubmit("andres", "mala-clave");

		const alert = await screen.findByRole("alert");
		expect(alert.textContent).toContain("credenciales inválidas");
	});
});
