/** Repos: guarda de proveedor review, campos GitLab condicionales y toggle de
 * conexión (mapa: dashboard.features.settings). */

import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/apiClient";
import type { Provider, Repo } from "../../lib/types";
import { createWrapper } from "../../test/createWrapper";
import { Repos } from "./Repos";

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

const reviewProvider: Provider = {
	id: 1,
	base_url: "https://api.openai.com/v1",
	model: "gpt-4o",
	api_key: "sk-***xyz",
	role: "review",
	priority: 1,
	enabled: true,
};

const repo: Repo = {
	id: 10,
	vcs: "github",
	external_id: 123,
	owner: "acme",
	name: "widget",
	enabled: true,
	review_drafts: false,
	language: "go",
	chat_org_only: false,
};

function mockBackend(providers: Provider[]) {
	mockedApi.mockImplementation((path: string, opts?: { method?: string }) => {
		if (path === "/api/repos" && opts?.method === "POST") {
			return Promise.resolve(repo);
		}
		if (path.startsWith("/api/repos/")) {
			return Promise.resolve(repo);
		}
		if (path === "/api/repos") return Promise.resolve([repo]);
		if (path === "/api/providers") return Promise.resolve(providers);
		return Promise.reject(new Error(`ruta inesperada: ${path}`));
	});
}

describe("Repos", () => {
	beforeEach(() => {
		mockedApi.mockReset();
	});

	afterEach(cleanup);

	it("sin proveedor review habilitado muestra la guarda y no conecta sin avisar", async () => {
		mockBackend([{ ...reviewProvider, enabled: false }]);

		render(<Repos />, { wrapper: createWrapper() });

		expect(
			await screen.findByText(/Necesitas al menos un proveedor LLM/),
		).toBeTruthy();
	});

	it("con proveedor review habilitado no muestra la guarda", async () => {
		mockBackend([reviewProvider]);

		render(<Repos />, { wrapper: createWrapper() });

		await screen.findByText("acme/widget");
		expect(
			screen.queryByText(/Necesitas al menos un proveedor LLM/),
		).toBeNull();
	});

	it("el modal de GitLab muestra los campos propios del VCS", async () => {
		mockBackend([reviewProvider]);

		render(<Repos />, { wrapper: createWrapper() });
		await screen.findByText("acme/widget");

		fireEvent.click(screen.getByText("Conectar repo"));
		expect(screen.queryByLabelText("Webhook secret")).toBeNull();

		fireEvent.input(screen.getByLabelText("VCS"), {
			target: { value: "gitlab" },
		});

		expect(screen.getByLabelText("Webhook secret")).toBeTruthy();
		expect(screen.getByLabelText(/API token/)).toBeTruthy();
		expect(screen.getByLabelText(/Deploy key/)).toBeTruthy();
		expect(screen.getByLabelText(/Base URL/)).toBeTruthy();
	});

	it("desconectar un repo es el flag enabled vía PUT", async () => {
		mockBackend([reviewProvider]);

		render(<Repos />, { wrapper: createWrapper() });
		await screen.findByText("acme/widget");

		fireEvent.click(screen.getByLabelText("Conectar acme/widget"));

		await waitFor(() => {
			expect(mockedApi).toHaveBeenCalledWith("/api/repos/10", {
				method: "PUT",
				body: { enabled: false },
			});
		});
	});
});
