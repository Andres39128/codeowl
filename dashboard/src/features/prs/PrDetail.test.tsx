/** PrDetail: resumen y mermaid como TEXTO, findings con estado de
 * verificación, diff degradable y ancla de hallazgo (mapa:
 * dashboard.features.prs). */

import { cleanup, fireEvent, render, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/apiClient";
import type { FindingView, PrDetailView } from "../../lib/types";
import { createWrapper } from "../../test/createWrapper";
import { PrDetail } from "./PrDetail";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));

vi.mock("../../lib/apiClient", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../lib/apiClient")>();
	return { ...actual, api: apiMock };
});

const review = {
	id: 1,
	status: "success" as const,
	summary: "Resumen del PR",
	walkthrough: "Paso 1\nPaso 2",
	mermaid: "sequenceDiagram\n  A->>B: hola",
	created_at: "2026-10-01T12:00:00Z",
};

const findings: FindingView[] = [
	{
		id: 1,
		file: "src/foo.go",
		line: 2,
		severity: "high",
		category: "bug",
		body: "Cuerpo alto",
		suggestion: "if err != nil {\n\treturn err\n}",
		source: "llm",
		verified: true,
	},
	{
		id: 2,
		file: "src/bar.go",
		line: 5,
		severity: "medium",
		category: "style",
		body: "Cuerpo medio",
		suggestion: null,
		source: "sast",
		verified: false,
	},
	{
		id: 3,
		file: "src/baz.go",
		line: 9,
		severity: "low",
		category: "nit",
		body: "Cuerpo bajo",
		suggestion: null,
		source: "llm",
		verified: null,
	},
];

const detail: PrDetailView = {
	pr: {
		id: 7,
		number: 42,
		author: "ana",
		state: "open",
		repo: { id: 1, owner: "acme", name: "codeowl", vcs: "github" },
		head_sha: "abc1234abcdef",
		base_ref: "main",
		updated_at: "2026-10-01T12:00:00Z",
		latest_review: null,
	},
	review,
	findings,
};

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

function mockApi(overrides: {
	detail?: PrDetailView;
	detailError?: Error;
	diff?: string;
	diffError?: Error;
}) {
	apiMock.mockImplementation((path: string) => {
		if (path === "/api/prs/7") {
			return overrides.detailError !== undefined
				? Promise.reject(overrides.detailError)
				: Promise.resolve(overrides.detail ?? detail);
		}
		if (path === "/api/prs/7/diff") {
			return overrides.diffError !== undefined
				? Promise.reject(overrides.diffError)
				: Promise.resolve({ diff: overrides.diff ?? diff });
		}
		return Promise.reject(new Error(`path inesperado: ${path}`));
	});
}

beforeEach(() => {
	apiMock.mockReset();
	Element.prototype.scrollIntoView = vi.fn();
});

afterEach(cleanup);

describe("PrDetail", () => {
	it("renderiza header, resumen y walkthrough; el mermaid queda como TEXTO", async () => {
		mockApi({});
		const { container } = render(<PrDetail prId={7} />, {
			wrapper: createWrapper(),
		});

		expect(await screen.findByText("PR #42 · acme/codeowl")).toBeTruthy();
		expect(screen.getByText(/● Abierto/)).toBeTruthy();
		expect(screen.getByText("ana · abc1234 → main")).toBeTruthy();
		expect(screen.getByText(/● Exitosa/)).toBeTruthy();
		expect(screen.getByText("Resumen del PR")).toBeTruthy();
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

	it("los findings muestran severidad, origen y los tres estados de verificación", async () => {
		mockApi({});
		const { container } = render(<PrDetail prId={7} />, {
			wrapper: createWrapper(),
		});

		expect(await screen.findByText("Cuerpo alto")).toBeTruthy();
		expect(screen.getByText(/● Alta/)).toBeTruthy();
		expect(screen.getByText(/● Media/)).toBeTruthy();
		expect(screen.getByText(/● Baja/)).toBeTruthy();
		expect(screen.getByText("SAST")).toBeTruthy();
		// LLM aparece en los dos findings de origen llm.
		expect(screen.getAllByText("LLM")).toHaveLength(2);
		expect(screen.getByText("✔ Verificado")).toBeTruthy();
		expect(screen.getByText("✖ Falso positivo (no publicado)")).toBeTruthy();
		expect(screen.getByText("— Sin verificar")).toBeTruthy();

		// La sugerencia del primer finding viaja en su bloque de código.
		const codeBlocks = Array.from(container.querySelectorAll("pre code")).map(
			(el) => el.textContent,
		);
		expect(codeBlocks.some((text) => text?.includes("return err"))).toBe(true);
	});

	it("PR sin corridas muestra el banner informativo en Resumen", async () => {
		mockApi({ detail: { ...detail, review: null, findings: [] } });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		expect(
			await screen.findByText("Este PR aún no tiene corridas de revisión."),
		).toBeTruthy();
	});

	it("404 del detalle muestra el mensaje específico", async () => {
		mockApi({ detailError: new ApiError(404, "PR inexistente") });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		expect(await screen.findByText("Pull request no encontrado.")).toBeTruthy();
	});

	it("diff con 502 degrada a banner y el resto de la página sigue", async () => {
		mockApi({ diffError: new ApiError(502, "el VCS no entregó el diff") });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		expect(
			await screen.findByText("No se pudo obtener el diff del VCS."),
		).toBeTruthy();
		expect(screen.getByText("Resumen del PR")).toBeTruthy();
		expect(screen.getByText("Cuerpo alto")).toBeTruthy();
	});

	it("click en file:line del finding ancla la fila del diff", async () => {
		mockApi({});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		fireEvent.click(await screen.findByText("src/foo.go:2"));

		const row = document.querySelector(
			'[data-file="src/foo.go"][data-line="2"]',
		);
		expect(row).not.toBeNull();
		expect(row?.scrollIntoView).toHaveBeenCalled();
	});
});
