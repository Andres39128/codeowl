/** PushDetail: header con before→after, resumen con mermaid como TEXTO,
 * hallazgos con "No aplica" que refresca el detalle y diff degradable
 * (trazabilidad-merges S1 — patrón de PrDetail). */

import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/apiClient";
import type {
	FindingView,
	PushDetailView,
	PushListItem,
	ReviewView,
} from "../../lib/types";
import { createWrapper } from "../../test/createWrapper";
import { PushDetail } from "./PushDetail";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));

vi.mock("../../lib/apiClient", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../lib/apiClient")>();
	return { ...actual, api: apiMock };
});

const push: PushListItem = {
	id: 3,
	repo: { id: 1, owner: "acme", name: "codeowl", vcs: "github" },
	branch: "main",
	before_sha: "1111111aaaaaaa",
	after_sha: "2222222bbbbbbb",
	committer: "ana",
	created_at: "2026-10-01T12:00:00Z",
	latest_review: {
		id: 9,
		status: "success",
		created_at: "2026-10-01T12:05:00Z",
	},
	findings_count: { high: 1, medium: 0, low: 1 },
};

const review: ReviewView = {
	id: 9,
	status: "success",
	summary: "Resumen del merge",
	walkthrough: "Paso 1\nPaso 2",
	mermaid: "sequenceDiagram\n  A->>B: hola",
	created_at: "2026-10-01T12:05:00Z",
};

// Hallazgos de la corrida 9: uno activo con sugerencia y uno dismissed (viaja
// igual para auditoría — la UI lo atenúa).
const findings: FindingView[] = [
	{
		id: 5,
		review_id: 9,
		file: "src/foo.go",
		line: 2,
		severity: "high",
		category: "logic",
		body: "Cuerpo alto",
		suggestion: "if err != nil {\n\treturn err\n}",
		source: "llm",
		verified: true,
		accepted: null,
		github_comment_id: null,
		dismissed: false,
		persistent: false,
	},
	{
		id: 6,
		review_id: 9,
		file: "src/bar.go",
		line: 5,
		severity: "low",
		category: "style",
		body: "Cuerpo bajo",
		suggestion: null,
		source: "sast",
		verified: null,
		accepted: null,
		github_comment_id: null,
		dismissed: true,
		persistent: false,
	},
];

const detail: PushDetailView = { push, review, findings };

// Incluye la línea ancla del primer finding (src/foo.go:2 → fila +).
const diff = [
	"diff --git a/src/foo.go b/src/foo.go",
	"--- a/src/foo.go",
	"+++ b/src/foo.go",
	"@@ -1,1 +1,2 @@",
	" context",
	"+added line",
	"",
].join("\n");

function mockApi(
	overrides: {
		detail?: PushDetailView;
		detailError?: Error;
		diff?: string;
		diffError?: Error;
	} = {},
) {
	apiMock.mockImplementation((path: string, opts?: { method?: string }) => {
		if (path === "/api/pushes/3") {
			return overrides.detailError !== undefined
				? Promise.reject(overrides.detailError)
				: Promise.resolve(overrides.detail ?? detail);
		}
		if (path === "/api/pushes/3/diff") {
			return overrides.diffError !== undefined
				? Promise.reject(overrides.diffError)
				: Promise.resolve({ diff: overrides.diff ?? diff });
		}
		if (path === "/api/findings/5/dismiss" && opts?.method === "POST") {
			return Promise.resolve({ dismissed: true });
		}
		return Promise.reject(new Error(`path inesperado: ${path}`));
	});
}

beforeEach(() => {
	apiMock.mockReset();
	Element.prototype.scrollIntoView = vi.fn();
});

afterEach(cleanup);

describe("PushDetail", () => {
	it("renderiza header, resumen y walkthrough; el mermaid queda como TEXTO", async () => {
		mockApi();
		const { container } = render(<PushDetail pushId={3} />, {
			wrapper: createWrapper(),
		});

		expect(
			await screen.findByText("Merge directo · acme/codeowl"),
		).toBeTruthy();
		expect(screen.getByText("ana · main · 1111111 → 2222222")).toBeTruthy();
		expect(screen.getByText(/● Exitosa/)).toBeTruthy();
		expect(screen.getByText("Resumen del merge")).toBeTruthy();
		expect(screen.getByText(/Paso 1/)).toBeTruthy();

		// §9.5: la fuente mermaid vive en un <pre><code>, sin svg ni canvas.
		const codeBlocks = Array.from(container.querySelectorAll("pre code")).map(
			(el) => el.textContent,
		);
		expect(codeBlocks.some((text) => text?.includes("sequenceDiagram"))).toBe(
			true,
		);
		expect(container.querySelector("svg")).toBeNull();
		expect(container.querySelector("canvas")).toBeNull();
	});

	it("los hallazgos muestran badges, sugerencia y el dismissed queda atenuado", async () => {
		mockApi();
		const { container } = render(<PushDetail pushId={3} />, {
			wrapper: createWrapper(),
		});

		expect(await screen.findByText("Cuerpo alto")).toBeTruthy();
		expect(screen.getAllByText(/● Alta/).length).toBeGreaterThanOrEqual(1);
		expect(screen.getAllByText(/● Baja/).length).toBeGreaterThanOrEqual(1);
		expect(screen.getByText("Lógica")).toBeTruthy();
		expect(screen.getByText("Estilo")).toBeTruthy();
		expect(screen.getByText("LLM")).toBeTruthy();
		expect(screen.getByText("SAST")).toBeTruthy();

		// Dismissed: atenuado y con su etiqueta (viaja igual — auditoría).
		const marcado = screen.getByText("Cuerpo bajo").closest("li");
		expect(marcado?.className).toContain("opacity-50");
		expect(screen.getByText("✕ No aplica")).toBeTruthy();

		// La sugerencia viaja en su bloque de código.
		const codeBlocks = Array.from(container.querySelectorAll("pre code")).map(
			(el) => el.textContent,
		);
		expect(codeBlocks.some((text) => text?.includes("return err"))).toBe(true);
	});

	it("el botón No aplica llama al endpoint y refresca el detalle", async () => {
		mockApi();
		render(<PushDetail pushId={3} />, { wrapper: createWrapper() });
		await screen.findByText("Cuerpo alto");

		const callsTo = (path: string) =>
			apiMock.mock.calls.filter(([p]) => p === path).length;
		expect(callsTo("/api/pushes/3")).toBe(1);

		fireEvent.click(screen.getByRole("button", { name: /no aplica/i }));

		await waitFor(() => {
			expect(apiMock).toHaveBeenCalledWith(
				"/api/findings/5/dismiss",
				expect.objectContaining({ method: "POST" }),
			);
		});
		// El invalidate del prefijo ["push"] refresca el detalle.
		await waitFor(() => {
			expect(callsTo("/api/pushes/3")).toBeGreaterThanOrEqual(2);
		});
	});

	it("push sin corridas muestra el banner informativo en Resumen", async () => {
		mockApi({ detail: { ...detail, review: null, findings: [] } });
		render(<PushDetail pushId={3} />, { wrapper: createWrapper() });

		expect(
			await screen.findByText("Este push aún no tiene corridas de revisión."),
		).toBeTruthy();
	});

	it("404 del detalle muestra el mensaje específico", async () => {
		mockApi({ detailError: new ApiError(404, "push inexistente") });
		render(<PushDetail pushId={3} />, { wrapper: createWrapper() });

		expect(await screen.findByText("Push no encontrado.")).toBeTruthy();
	});

	it("diff con 502 degrada a banner y el resto de la página sigue", async () => {
		mockApi({ diffError: new ApiError(502, "el VCS no entregó el diff") });
		render(<PushDetail pushId={3} />, { wrapper: createWrapper() });

		expect(
			await screen.findByText("No se pudo obtener el diff del VCS."),
		).toBeTruthy();
		expect(screen.getByText("Resumen del merge")).toBeTruthy();
		expect(screen.getByText("Cuerpo alto")).toBeTruthy();
	});

	it("click en file:line del hallazgo ancla la fila del diff", async () => {
		mockApi();
		render(<PushDetail pushId={3} />, { wrapper: createWrapper() });

		fireEvent.click(await screen.findByText("src/foo.go:2"));

		const row = document.querySelector(
			'[data-file="src/foo.go"][data-line="2"]',
		);
		expect(row).not.toBeNull();
		expect(row?.scrollIntoView).toHaveBeenCalled();
	});
});

// Arreglar con IA en hallazgos de push (trazabilidad): mismo creador de
// prompts que un PR, con contexto honesto — sin PR no dice "pull request #N".
describe("arreglar con IA en pushes", () => {
	it("abre el modal con el prompt de corrección y contexto de push", async () => {
		mockApi();
		render(<PushDetail pushId={3} />, { wrapper: createWrapper() });

		const botones = await screen.findAllByRole("button", {
			name: /arreglar con ia/i,
		});
		expect(botones.length).toBeGreaterThan(0);
		fireEvent.click(botones[0] as HTMLElement);

		// El modal trae el prompt completo con el contexto del repo.
		expect(screen.getByRole("dialog")).toBeTruthy();
		expect(screen.getByText(/Repositorio acme\/codeowl/)).toBeTruthy();
		// Sin PR el prompt no inventa número.
		expect(screen.queryByText(/pull request #/)).toBeNull();
		// Trae el hallazgo y el hunk del diff del archivo.
		const dialogo = screen.getByRole("dialog");
		expect(dialogo.textContent).toContain("src/foo.go");

		// Tab Explicar cambia el prompt.
		fireEvent.click(screen.getByRole("tab", { name: "Explicar" }));
		expect(
			screen.getByText(/Explicame como si no supiera programar/),
		).toBeTruthy();
	});
});
