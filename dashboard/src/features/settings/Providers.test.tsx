/** Providers: listado, alta, toggle de habilitación y prueba de conexión
 * (mapa: dashboard.features.settings). */

import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/apiClient";
import type { Provider } from "../../lib/types";
import { createWrapper } from "../../test/createWrapper";
import { Providers } from "./Providers";

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
}));

const mockedApi = api as unknown as ReturnType<typeof vi.fn>;

const provider: Provider = {
	id: 1,
	base_url: "https://api.openai.com/v1",
	model: "gpt-4o",
	api_key: "sk-***xyz",
	role: "review",
	priority: 1,
	enabled: true,
};

function fillField(label: string, value: string) {
	fireEvent.input(screen.getByLabelText(label), {
		target: { value },
	});
}

describe("Providers", () => {
	beforeEach(() => {
		mockedApi.mockReset();
		mockedApi.mockResolvedValue([provider]);
	});

	afterEach(cleanup);

	it("lista los proveedores con rol y prioridad", async () => {
		render(<Providers />, { wrapper: createWrapper() });

		expect(await screen.findByText("gpt-4o")).toBeTruthy();
		expect(screen.getByText("review")).toBeTruthy();
		expect(screen.getByText("1")).toBeTruthy();
	});

	it("alta: envía POST con el formulario y cierra el modal", async () => {
		render(<Providers />, { wrapper: createWrapper() });
		await screen.findByText("gpt-4o");

		fireEvent.click(screen.getByText("Agregar proveedor"));
		fillField("Base URL", "https://api.deepseek.com");
		fillField("Modelo", "deepseek-chat");
		fillField("API Key", "sk-nueva");
		fillField("Prioridad", "2");
		fireEvent.click(screen.getByText("Guardar"));

		await waitFor(() => {
			expect(mockedApi).toHaveBeenCalledWith("/api/providers", {
				method: "POST",
				body: {
					base_url: "https://api.deepseek.com",
					model: "deepseek-chat",
					api_key: "sk-nueva",
					role: "review",
					priority: 2,
					enabled: true,
				},
			});
		});
		await waitFor(() => {
			expect(screen.queryByText("Guardar")).toBeNull();
		});
	});

	it("toggle de habilitación: PUT completo con key vacía (conserva la actual)", async () => {
		render(<Providers />, { wrapper: createWrapper() });
		await screen.findByText("gpt-4o");

		fireEvent.click(screen.getByLabelText("Habilitar proveedor gpt-4o"));

		await waitFor(() => {
			expect(mockedApi).toHaveBeenCalledWith("/api/providers/1", {
				method: "PUT",
				body: {
					base_url: "https://api.openai.com/v1",
					model: "gpt-4o",
					api_key: "",
					role: "review",
					priority: 1,
					enabled: false,
				},
			});
		});
	});

	it("prueba de conexión muestra el resultado en línea", async () => {
		mockedApi.mockImplementation((path: string, opts?: { method?: string }) => {
			if (path === "/api/providers/1/test" && opts?.method === "POST") {
				return Promise.resolve({ ok: true, latency_ms: 42 });
			}
			return Promise.resolve([provider]);
		});

		render(<Providers />, { wrapper: createWrapper() });
		await screen.findByText("gpt-4o");

		fireEvent.click(screen.getByText("Probar"));

		expect(await screen.findByText("OK · 42 ms")).toBeTruthy();
	});
});
