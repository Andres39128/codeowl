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
import type { Provider, Repo, RepoHealth } from "../../lib/types";
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

function mockBackend(providers: Provider[], health: RepoHealth | null = null) {
	mockedApi.mockImplementation((path: string, opts?: { method?: string }) => {
		if (path === "/api/repos" && opts?.method === "POST") {
			return Promise.resolve(repo);
		}
		if (path.endsWith("/health")) {
			return health === null
				? Promise.reject(new Error("sin salud"))
				: Promise.resolve(health);
		}
		if (path.endsWith("/review") && opts?.method === "POST") {
			return Promise.resolve({ enqueued: true, pr_number: 7 });
		}
		if (path.startsWith("/api/repos/")) {
			return Promise.resolve(repo);
		}
		if (path === "/api/repos") return Promise.resolve([repo]);
		if (path === "/api/providers") return Promise.resolve(providers);
		return Promise.reject(new Error(`ruta inesperada: ${path}`));
	});
}

const saludVerde: RepoHealth = {
	app_installed: true,
	last_delivery_at: "2026-10-23T12:00:00Z",
	has_review_provider: true,
	open_prs: 1,
	last_review_at: "2026-10-23T11:00:00Z",
	latest_open_pr: {
		number: 7,
		head_sha: "abc123",
		base_sha: "def456",
	},
};

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
	it("muestra los checks de salud del repo con remedios", async () => {
		mockBackend([reviewProvider], {
			...saludVerde,
			app_installed: false,
			has_review_provider: false,
			last_delivery_at: null,
		});

		render(<Repos />, { wrapper: createWrapper() });

		expect(await screen.findByText(/App sin instalar/)).toBeTruthy();
		expect(screen.getByText(/instalala en el repo/)).toBeTruthy();
		expect(screen.getByText(/Sin webhook recibido/)).toBeTruthy();
		expect(screen.getByText(/Sin proveedor review/)).toBeTruthy();
	});

	it("todo verde muestra ok y el botón revisar ahora encola el PR abierto", async () => {
		mockBackend([reviewProvider], saludVerde);

		render(<Repos />, { wrapper: createWrapper() });

		const boton = await screen.findByRole("button", { name: /revisar ahora/i });
		fireEvent.click(boton);

		await waitFor(() => {
			expect(mockedApi).toHaveBeenCalledWith(
				"/api/repos/10/review",
				expect.objectContaining({ method: "POST" }),
			);
		});
	});

	it("sin PR abierto el botón está deshabilitado y lo explica", async () => {
		mockBackend([reviewProvider], {
			...saludVerde,
			open_prs: 0,
			latest_open_pr: null,
		});

		render(<Repos />, { wrapper: createWrapper() });

		const boton = await screen.findByRole("button", { name: /revisar ahora/i });
		expect(boton.hasAttribute("disabled")).toBe(true);
		expect(screen.getByText(/sin PRs abiertos/i)).toBeTruthy();
	});
});
