/** PrDetail: resumen y mermaid como TEXTO, findings con estado de
 * verificación, diff degradable y ancla de hallazgo (mapa:
 * dashboard.features.prs). */

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
	PrDetailView,
	ReviewDetail,
	StackCohort,
} from "../../lib/types";
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

// Findings de la corrida 1 (review_id 1). Reflejan el dominio: los hallazgos
// del LLM pasan por el Verifier (verificado / falso positivo / sin
// verificar) y los del linter son deterministas (nunca verificados).
const findings: FindingView[] = [
	{
		id: 1,
		review_id: 1,
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
		id: 2,
		review_id: 1,
		file: "src/bar.go",
		line: 5,
		severity: "medium",
		category: "style",
		body: "Cuerpo medio",
		suggestion: null,
		source: "llm",
		verified: false,
		accepted: null,
		github_comment_id: null,
		dismissed: false,
		persistent: false,
	},
	{
		id: 3,
		review_id: 1,
		file: "src/baz.go",
		line: 9,
		severity: "low",
		category: "tests",
		body: "Cuerpo bajo",
		suggestion: null,
		source: "llm",
		verified: null,
		accepted: null,
		github_comment_id: null,
		dismissed: false,
		persistent: false,
	},
	{
		id: 4,
		review_id: 1,
		file: "src/lint.go",
		line: 1,
		severity: "high",
		category: "security",
		body: "Cuerpo del linter",
		suggestion: null,
		source: "sast",
		verified: null,
		accepted: null,
		github_comment_id: null,
		dismissed: false,
		persistent: false,
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
		risk_score: null,
		effort_score: null,
		impact_score: null,
		latest_review: null,
	},
	review,
	findings,
	resolved: [],
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

// Filtros de hallazgos (visualización para revisar código): con cientos de
// hallazgos, el revisor filtra por severidad, origen, archivo y texto libre.
// La UI explica la semántica de cada badge: los linters (SAST) son
// deterministas y no se verifican; los del LLM pasan por el Verifier.
describe("ayuda de los hallazgos", () => {
	it("la sugerencia se explica y ofrece copiar y el enlace al comentario", async () => {
		const conComentario: PrDetailView = {
			...detail,
			findings: [
				{
					...findings[0],
					github_comment_id: "4363709304",
					accepted: false,
				} as FindingView,
			],
		};
		mockApi({ detail: conComentario, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		await screen.findByText("Cuerpo alto");
		// Explica qué es la sugerencia.
		expect(screen.getByText(/propuesta de código/i)).toBeTruthy();
		// Copiar el código.
		expect(screen.getByRole("button", { name: /copiar/i })).toBeTruthy();
		// Enlace al comentario real del PR, donde GitHub lo aplica con un clic.
		const enlace = screen.getByRole("link", { name: /ver en github/i });
		expect(enlace.getAttribute("href")).toContain(
			"https://github.com/acme/codeowl/pull/42#discussion_r4363709304",
		);
		// Distingue la sugerencia no aplicada de la aplicada.
		expect(screen.getByText(/no aplicada/i)).toBeTruthy();
	});

	it("marca la sugerencia aplicada cuando el metrics job la evaluó", async () => {
		const aplicada: PrDetailView = {
			...detail,
			findings: [{ ...findings[0], accepted: true } as FindingView],
		};
		mockApi({ detail: aplicada, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		await screen.findByText("Cuerpo alto");
		expect(screen.getByText(/aplicada/i)).toBeTruthy();
		expect(screen.queryByText(/no aplicada/i)).toBeNull();
	});
	it("los hallazgos SAST se marcan como deterministas, no como sin verificar", async () => {
		mockApi({ detail, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		await screen.findByText("Cuerpo del linter");
		expect(screen.getByText("Determinista (SAST)")).toBeTruthy();
		// El hallazgo LLM sin verificar conserva su propio estado: los dos
		// conviven (uno es determinista, el otro no se pudo contrastar).
		expect(screen.getByText("— Sin verificar")).toBeTruthy();
	});

	it("el hallazgo LLM sin verificar explica por qué", async () => {
		mockApi({ detail, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		await screen.findByText("Cuerpo bajo");
		const sinVerificar = screen.getByText("— Sin verificar");
		expect(sinVerificar).toBeTruthy();
		expect(sinVerificar.getAttribute("title")).toContain(
			"No se pudo contrastar",
		);
	});

	it("la leyenda explica cómo leer los badges", async () => {
		mockApi({ detail, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		const leyenda = (await screen.findByText(/Cómo leer esto/))?.closest("p");
		expect(leyenda).toBeTruthy();
		expect(leyenda?.textContent).toContain("SAST");
		expect(leyenda?.textContent).toContain("Verificación");
	});

	it("la categoría se muestra traducida y con su significado", async () => {
		mockApi({ detail, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		await screen.findByText("Cuerpo alto");
		// "logic" → "Lógica", "style" → "Estilo" (el conjunto cerrado es en
		// inglés; la UI lo traduce y deja el dato crudo a mano).
		expect(screen.getByText("Lógica")).toBeTruthy();
		expect(screen.getByText("Estilo")).toBeTruthy();
	});
});

describe("filtros de hallazgos", () => {
	it("filtra por severidad y por texto libre", async () => {
		mockApi({ detail, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		expect(await screen.findByText("Cuerpo alto")).toBeTruthy();
		expect(screen.getByText("Cuerpo medio")).toBeTruthy();
		expect(screen.getByText("Cuerpo bajo")).toBeTruthy();

		// Severidad: solo quedan los altos.
		fireEvent.input(screen.getByLabelText("Severidad"), {
			target: { value: "high" },
		});
		expect(screen.getByText("Cuerpo alto")).toBeTruthy();
		expect(screen.queryByText("Cuerpo medio")).toBeNull();

		// Texto libre: busca en el cuerpo del hallazgo.
		fireEvent.input(screen.getByLabelText("Severidad"), {
			target: { value: "" },
		});
		fireEvent.input(screen.getByLabelText("Buscar"), {
			target: { value: "medio" },
		});
		expect(screen.getByText("Cuerpo medio")).toBeTruthy();
		expect(screen.queryByText("Cuerpo alto")).toBeNull();
	});

	it("filtra por origen y por archivo", async () => {
		mockApi({ detail, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		await screen.findByText("Cuerpo alto");
		fireEvent.input(screen.getByLabelText("Origen"), {
			target: { value: "sast" },
		});
		expect(screen.getByText("Cuerpo del linter")).toBeTruthy();
		expect(screen.queryByText("Cuerpo alto")).toBeNull();

		fireEvent.input(screen.getByLabelText("Origen"), {
			target: { value: "" },
		});
		fireEvent.input(screen.getByLabelText("Archivo"), {
			target: { value: "src/baz.go" },
		});
		expect(screen.getByText("Cuerpo bajo")).toBeTruthy();
		expect(screen.queryByText("Cuerpo alto")).toBeNull();
	});

	it("muestra cuántos hallazgos quedan del total", async () => {
		mockApi({ detail, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		await screen.findByText("Cuerpo alto");
		expect(screen.getByText("Hallazgos (4)")).toBeTruthy();
		fireEvent.input(screen.getByLabelText("Severidad"), {
			target: { value: "low" },
		});
		expect(screen.getByText("Hallazgos (1 de 4)")).toBeTruthy();
	});

	it("muestra por defecto solo la última corrida y abre el histórico", async () => {
		const viejo: FindingView = {
			id: 99,
			review_id: 999,
			file: "src/foo.go",
			line: 2,
			severity: "high",
			category: "logic",
			body: "Hallazgo de una corrida vieja",
			suggestion: null,
			source: "llm",
			verified: null,
			accepted: null,
			github_comment_id: null,
			dismissed: false,
			persistent: false,
		};
		const dosCorridas: PrDetailView = {
			...detail,
			findings: [...findings, viejo],
		};
		mockApi({ detail: dosCorridas, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		// Por defecto: solo los de la última corrida (review_id 1).
		await screen.findByText("Cuerpo alto");
		expect(screen.getByText("Hallazgos (4)")).toBeTruthy();
		expect(screen.queryByText("Hallazgo de una corrida vieja")).toBeNull();

		// El toggle abre el histórico completo: 4 de la última + 1 viejo.
		fireEvent.click(screen.getByLabelText("Corrida"));
		expect(screen.getByText("Hallazgos (5)")).toBeTruthy();
		expect(screen.getByText("Hallazgo de una corrida vieja")).toBeTruthy();
	});
});

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
		expect(screen.getAllByText(/● Alta/).length).toBeGreaterThanOrEqual(1);
		expect(screen.getAllByText(/● Media/).length).toBeGreaterThanOrEqual(1);
		expect(screen.getAllByText(/● Baja/).length).toBeGreaterThanOrEqual(1);
		// El badge de origen: el filtro de origen tiene una opción con el
		// mismo texto, así que se toma el badge (no el <option>).
		expect(screen.getAllByText("SAST").length).toBeGreaterThanOrEqual(1);
		// LLM aparece en los dos findings de origen llm (badge + opción).
		expect(screen.getAllByText("LLM").length).toBeGreaterThanOrEqual(2);
		expect(screen.getByText("✔ Verificado")).toBeTruthy();
		expect(screen.getByText("✖ Falso positivo (no publicado)")).toBeTruthy();
		expect(screen.getByText("— Sin verificar")).toBeTruthy();
		// El hallazgo de linter es determinista: no se verifica (§3.3).
		expect(screen.getByText("Determinista (SAST)")).toBeTruthy();

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

	// Change Stack (F6 §6): la Card solo existe cuando el detalle trae
	// cohortes — sin índice del repo el campo viene ausente y no se renderiza
	// nada (degradación visible, sin placeholders vacíos).
	const changeStack: StackCohort[] = [
		{ level: 0, kind: "code", files: ["pkg/util/util.go"] },
		{ level: 1, kind: "code", files: ["cmd/c.go"] },
		{ level: 2, kind: "tests", files: ["cmd/c_test.go"] },
	];

	it("con change_stack renderiza la Card con niveles, Pruebas y archivos", async () => {
		mockApi({ detail: { ...detail, change_stack: changeStack } });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		expect(await screen.findByText("Change Stack")).toBeTruthy();
		expect(screen.getByText("Nivel 0")).toBeTruthy();
		expect(screen.getByText("Nivel 1")).toBeTruthy();
		expect(screen.getByText("Nivel 2")).toBeTruthy();
		// kind tests → etiqueta Pruebas (§5.2.3: el estado no vive solo en el color).
		expect(screen.getAllByText("Pruebas").length).toBeGreaterThanOrEqual(1);
		// Archivos como lista mono apagada.
		expect(screen.getByText("pkg/util/util.go")).toBeTruthy();
		expect(screen.getByText("cmd/c_test.go")).toBeTruthy();
	});

	it("sin change_stack no renderiza la Card", async () => {
		mockApi({});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		expect(await screen.findByText("Cuerpo alto")).toBeTruthy();
		expect(screen.queryByText("Change Stack")).toBeNull();
	});
});

describe("detalle de corrida", () => {
	it("el resumen ofrece ver la auditoría de la última corrida", async () => {
		const detalle: ReviewDetail = {
			id: 1,
			pull_request_id: 7,
			status: "success",
			created_at: "2026-10-01T12:00:00Z",
			duration_ms: 301146,
			summary: "s",
			llm_usage: [
				{
					role: "review",
					model: "Muse-Glimmer-30B",
					tokens_in: 5552,
					tokens_out: 17215,
				},
				{
					role: "cheap",
					model: "Muse-Glimmer-30B",
					tokens_in: 5064,
					tokens_out: 3614,
				},
			],
			findings_by_severity: { high: 5, medium: 4, low: 0 },
		};
		apiMock.mockImplementation((path: string) => {
			if (path === "/api/reviews/1") return Promise.resolve(detalle);
			if (path === "/api/prs/7") return Promise.resolve(detail);
			if (path === "/api/prs/7/diff") return Promise.resolve({ diff });
			return Promise.reject(new Error(`path inesperado: ${path}`));
		});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		fireEvent.click(
			await screen.findByRole("button", { name: /ver corrida/i }),
		);

		expect(await screen.findByText(/5m 1s/)).toBeTruthy();
		expect(screen.getByText(/review/)).toBeTruthy();
		// El separador de miles depende del locale del runner: se asserta
		// sobre el total crudo (5552+17215+5064+3614 = 31445).
		expect(screen.getByText(/31[.,]445 tokens/)).toBeTruthy();
		expect(screen.getByText(/5 alta/)).toBeTruthy();
	});
});

describe("triage de hallazgos (S4)", () => {
	it("marca no aplica y oculta los marcados por defecto", async () => {
		const marcado: FindingView = {
			id: 5,
			review_id: 1,
			file: "src/marcado.go",
			line: 1,
			severity: "low",
			category: "style",
			body: "Cuerpo marcado",
			suggestion: null,
			source: "llm",
			verified: null,
			accepted: null,
			github_comment_id: null,
			dismissed: true,
			persistent: false,
		};
		apiMock.mockImplementation((path: string) => {
			if (path === "/api/prs/7")
				return Promise.resolve({ ...detail, findings: [...findings, marcado] });
			if (path === "/api/prs/7/diff") return Promise.resolve({ diff });
			return Promise.reject(new Error(`path inesperado: ${path}`));
		});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		// El marcado está oculto por defecto: 5 hallazgos, 4 visibles.
		await screen.findByText("Cuerpo alto");
		expect(screen.queryByText("Cuerpo marcado")).toBeNull();
		expect(screen.getByText("Hallazgos (4 de 5)")).toBeTruthy();

		// Mostrar marcados lo revela con su etiqueta.
		fireEvent.click(screen.getByLabelText("Marcados"));
		expect(screen.getByText("Cuerpo marcado")).toBeTruthy();
		expect(screen.getByText("✕ No aplica")).toBeTruthy();
	});

	it("el botón no aplica llama al endpoint y refresca", async () => {
		apiMock.mockImplementation((path: string, opts?: { method?: string }) => {
			if (path === "/api/findings/2/dismiss" && opts?.method === "POST") {
				return Promise.resolve({ dismissed: true });
			}
			if (path === "/api/prs/7") return Promise.resolve(detail);
			if (path === "/api/prs/7/diff") return Promise.resolve({ diff });
			return Promise.reject(new Error(`path inesperado: ${path}`));
		});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });
		await screen.findByText("Cuerpo medio");

		const botones = screen.getAllByRole("button", { name: /no aplica/i });
		fireEvent.click(botones[0]!);

		await waitFor(() => {
			expect(apiMock).toHaveBeenCalledWith(
				"/api/findings/1/dismiss",
				expect.objectContaining({ method: "POST" }),
			);
		});
	});

	it("agrupa por archivo con encabezados colapsables", async () => {
		apiMock.mockImplementation((path: string) => {
			if (path === "/api/prs/7") return Promise.resolve(detail);
			if (path === "/api/prs/7/diff") return Promise.resolve({ diff });
			return Promise.reject(new Error(`path inesperado: ${path}`));
		});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });
		await screen.findByText("Cuerpo alto");

		fireEvent.click(screen.getByLabelText("Vista"));

		const grupos = screen.getAllByRole("group");
		expect(grupos.length).toBe(4); // un details por archivo
		// El grupo es el <details> del archivo; el texto vive en el summary.
		expect(
			screen.getAllByText(/src\/foo\.go/).some((el) => el.closest("details")),
		).toBe(true);
	});
});

describe("creador de prompts (S1)", () => {
	it("abre el modal con el prompt de corrección listo para copiar", async () => {
		apiMock.mockImplementation((path: string) => {
			if (path === "/api/prs/7") return Promise.resolve(detail);
			if (path === "/api/prs/7/diff") return Promise.resolve({ diff });
			return Promise.reject(new Error(`path inesperado: ${path}`));
		});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });
		await screen.findByText("Cuerpo alto");

		fireEvent.click(
			screen.getAllByRole("button", { name: /arreglar con ia/i })[0]!,
		);

		// El modal trae el prompt con el contexto del hallazgo.
		const modal = await screen.findByRole("dialog");
		expect(modal.textContent).toContain("Cuerpo alto");
		expect(modal.textContent).toContain("src/foo.go");
		expect(modal.textContent).toContain("return err"); // la sugerencia del fixture
		expect(modal.textContent).toContain("explicación en lenguaje simple");
		// El diff del archivo viaja en el prompt.
		expect(modal.textContent).toContain("+added line");
	});

	it("el prompt de explicar no pide código", async () => {
		apiMock.mockImplementation((path: string) => {
			if (path === "/api/prs/7") return Promise.resolve(detail);
			if (path === "/api/prs/7/diff") return Promise.resolve({ diff });
			return Promise.reject(new Error(`path inesperado: ${path}`));
		});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });
		await screen.findByText("Cuerpo alto");

		fireEvent.click(
			screen.getAllByRole("button", { name: /arreglar con ia/i })[0]!,
		);
		fireEvent.click(await screen.findByRole("tab", { name: /explicar/i }));

		const modal = screen.getByRole("dialog");
		expect(modal.textContent).toContain("como si no supiera programar");
		expect(modal.textContent).not.toContain("listo para reemplazar");
	});

	it("copiar lleva el prompt al portapapeles", async () => {
		const writeText = vi.fn().mockResolvedValue(undefined);
		vi.stubGlobal("navigator", { clipboard: { writeText } });
		apiMock.mockImplementation((path: string) => {
			if (path === "/api/prs/7") return Promise.resolve(detail);
			if (path === "/api/prs/7/diff") return Promise.resolve({ diff });
			return Promise.reject(new Error(`path inesperado: ${path}`));
		});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });
		await screen.findByText("Cuerpo alto");

		fireEvent.click(
			screen.getAllByRole("button", { name: /arreglar con ia/i })[0]!,
		);
		fireEvent.click(
			await screen.findByRole("button", { name: /^copiar prompt$/i }),
		);

		await waitFor(() => {
			expect(writeText).toHaveBeenCalled();
			const prompt = writeText.mock.calls[0]![0] as string;
			expect(prompt).toContain("Cuerpo alto");
			expect(prompt).toContain("src/foo.go");
		});
		vi.unstubAllGlobals();
	});
});

describe("reintentar corrida (S5)", () => {
	it("con corrida failed muestra el botón y encola el reintento", async () => {
		const fallida: PrDetailView = {
			...detail,
			review: detail.review
				? { ...detail.review, status: "failed" as const }
				: null,
		};
		apiMock.mockImplementation((path: string, opts?: { method?: string }) => {
			if (path === "/api/prs/7/review" && opts?.method === "POST") {
				return Promise.resolve({ enqueued: true });
			}
			if (path === "/api/prs/7") return Promise.resolve(fallida);
			if (path === "/api/prs/7/diff") return Promise.resolve({ diff });
			return Promise.reject(new Error(`path inesperado: ${path}`));
		});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		fireEvent.click(
			await screen.findByRole("button", { name: /reintentar corrida/i }),
		);

		await waitFor(() => {
			expect(apiMock).toHaveBeenCalledWith(
				"/api/prs/7/review",
				expect.objectContaining({ method: "POST" }),
			);
		});
		expect(await screen.findByText(/encolada/i)).toBeTruthy();
	});

	it("con corrida exitosa no muestra el botón", async () => {
		apiMock.mockImplementation((path: string) => {
			if (path === "/api/prs/7") return Promise.resolve(detail);
			if (path === "/api/prs/7/diff") return Promise.resolve({ diff });
			return Promise.reject(new Error(`path inesperado: ${path}`));
		});
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		await screen.findByText("Cuerpo alto");
		expect(
			screen.queryByRole("button", { name: /reintentar corrida/i }),
		).toBeNull();
	});
});

// Resolución entre corridas (resolucion-hallazgos S2): badge Persistente en
// los hallazgos que reaparecieron y sección colapsada con los resueltos de la
// corrida anterior.
describe("resolución entre corridas", () => {
	it("marca el hallazgo persistente y no al nuevo", async () => {
		const conPersistente: PrDetailView = {
			...detail,
			findings: [
				...findings,
				{
					id: 9,
					review_id: 1,
					file: "src/reaparece.go",
					line: 7,
					severity: "medium",
					category: "style",
					body: "Otra vez lo mismo",
					suggestion: null,
					source: "sast",
					verified: null,
					accepted: null,
					github_comment_id: null,
					dismissed: false,
					persistent: true,
				} as FindingView,
			],
			resolved: [],
		};
		mockApi({ detail: conPersistente, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		await screen.findByText("Otra vez lo mismo");
		// El badge viaja solo con el hallazgo persistente.
		expect(screen.getByText(/persistente/i)).toBeTruthy();
		// Sin resueltos, la sección no existe.
		expect(screen.queryByText(/resueltos en esta corrida/i)).toBeNull();
	});

	it("lista colapsada los resueltos de la corrida anterior", async () => {
		const conResueltos: PrDetailView = {
			...detail,
			resolved: [
				{
					id: 99,
					review_id: 1,
					file: "src/viejo.go",
					line: 4,
					severity: "medium",
					category: "style",
					body: "Ya no está en la última corrida",
					suggestion: null,
					source: "sast",
					verified: null,
					accepted: null,
					github_comment_id: null,
					dismissed: false,
					persistent: false,
				} as FindingView,
			],
		};
		mockApi({ detail: conResueltos, diff });
		render(<PrDetail prId={7} />, { wrapper: createWrapper() });

		await screen.findByText("Resueltos en esta corrida (1)");
		expect(screen.getByText("src/viejo.go:4")).toBeTruthy();
		expect(screen.getByText("Ya no está en la última corrida")).toBeTruthy();
	});
});
