/** Triage: métricas de los últimos 30 días, badges de riesgo por bandas,
 * filtros de estado (server) y de severidad/repo (client-side), mensajes de
 * vacío y navegación a la fila del detalle (mapa: dashboard.features.triage). */

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { MetricsView, PrView } from "../../lib/types";
import { createWrapper } from "../../test/createWrapper";
import { Triage } from "./Triage";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));

vi.mock("../../lib/apiClient", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../lib/apiClient")>();
	return { ...actual, api: apiMock };
});

function makePr(overrides: Partial<PrView> = {}): PrView {
	return {
		id: 101,
		number: 101,
		author: "ana",
		state: "open",
		repo: { id: 1, owner: "acme", name: "codeowl", vcs: "github" },
		head_sha: "abc1234abcdef",
		base_ref: "main",
		updated_at: "2026-10-01T12:00:00Z",
		risk_score: 50,
		effort_score: 20,
		impact_score: null,
		latest_review: {
			id: 1,
			status: "success",
			created_at: "2026-10-01T12:00:00Z",
			counts: { high: 0, medium: 2, low: 0 },
		},
		...overrides,
	};
}

const metrics: MetricsView = {
	window_days: 30,
	merged_prs: 4,
	avg_cycle_time_hours: 24,
	findings_with_outcome: 8,
	accepted: 6,
	accepted_rate: 0.75,
	resolved_comments: 10,
	false_positives: 2,
	false_positive_rate: 0.2,
	reviews_with_cost: 5,
	avg_tokens_per_review: 1500,
};

// Filas ya ordenadas por riesgo como las sirve el server (sort=risk): el
// cliente NO reordena, solo filtra severidad y repo.
const prsByRisk = [
	makePr({
		risk_score: 85,
		latest_review: {
			id: 1,
			status: "success",
			created_at: "2026-10-01T12:00:00Z",
			counts: { high: 3, medium: 0, low: 1 },
		},
	}),
	makePr({ id: 102, number: 102, author: "beto" }),
	makePr({
		id: 103,
		number: 103,
		author: "cara",
		risk_score: 10,
		repo: { id: 2, owner: "acme", name: "otro-repo", vcs: "gitlab" },
		latest_review: {
			id: 3,
			status: "failed",
			created_at: "2026-09-30T09:00:00Z",
			counts: { high: 0, medium: 0, low: 1 },
		},
	}),
	makePr({
		id: 104,
		number: 104,
		author: "dana",
		risk_score: null,
		latest_review: null,
	}),
];

function mockApi(list: PrView[], metricsData: MetricsView) {
	apiMock.mockImplementation((path: string) =>
		path.startsWith("/api/metrics")
			? Promise.resolve(metricsData)
			: Promise.resolve(list),
	);
}

beforeEach(() => {
	apiMock.mockReset();
	location.hash = "";
});

// Columnas esfuerzo/impacto del triage extendido (F6): mismas bandas que el
// riesgo (0-100 → Baja/Media/Alta); null → "—", nunca banda falsa.
it("muestra las columnas de esfuerzo e impacto (F6)", async () => {
	mockApi(
		[
			makePr({ effort_score: 70, impact_score: 45 }),
			makePr({ id: 102, number: 102, effort_score: null, impact_score: null }),
		],
		metrics,
	);
	render(<Triage />, { wrapper: createWrapper() });
	expect(await screen.findByText("Esfuerzo")).toBeTruthy();
	expect(screen.getByText("Impacto")).toBeTruthy();
	// 70 → Alta (esfuerzo); 50 (riesgo default) y 45 (impacto) → Media.
	expect((await screen.findAllByText("● Alta")).length).toBeGreaterThanOrEqual(1);
	expect(screen.getAllByText("● Media").length).toBeGreaterThanOrEqual(2);
});

// El selector de orden viaja al server (F6): riesgo (default), esfuerzo o
// impacto → sort=risk|effort|impact en /api/prs.
it("cambia el orden del listado vía server (F6)", async () => {
	mockApi([], metrics);
	render(<Triage />, { wrapper: createWrapper() });
	const orden = await screen.findByLabelText("Orden");
	fireEvent.input(orden, { target: { value: "esfuerzo" } });
	await waitFor(() =>
		expect(apiMock).toHaveBeenCalledWith("/api/prs?sort=effort&state=open"),
	);
});

afterEach(cleanup);

describe("Triage", () => {
	it("muestra las métricas de los últimos 30 días con formato es", async () => {
		mockApi(prsByRisk, metrics);
		render(<Triage />, { wrapper: createWrapper() });

		expect(await screen.findByText("Métricas (últimos 30 días)")).toBeTruthy();
		expect(screen.getByText("24,0")).toBeTruthy();
		expect(screen.getByText("75%")).toBeTruthy();
		expect(screen.getByText("20%")).toBeTruthy();
		expect(screen.getByText("1.500")).toBeTruthy();
	});

	it("métricas sin datos suficientes muestran —, no 0", async () => {
		mockApi([], {
			...metrics,
			avg_cycle_time_hours: null,
			accepted_rate: null,
			false_positive_rate: null,
			avg_tokens_per_review: null,
		});
		render(<Triage />, { wrapper: createWrapper() });

		expect(await screen.findAllByTitle("Sin datos suficientes")).toHaveLength(
			4,
		);
		expect(screen.getByText(/Todavía no hay pull requests/)).toBeTruthy();
	});

	it("lista los PRs con badges de riesgo por banda y el orden del server", async () => {
		mockApi(prsByRisk, metrics);
		render(<Triage />, { wrapper: createWrapper() });

		await screen.findByText("Métricas (últimos 30 días)");
		// Con las columnas F6 (esfuerzo/impacto) puede haber varias celdas con
		// la misma banda — el riesgo trae las tres, el esfuerzo default 20 (Baja).
		expect(screen.getAllByText("● Alta").length).toBeGreaterThanOrEqual(1);
		expect(screen.getAllByText("● Media").length).toBeGreaterThanOrEqual(1);
		expect(screen.getAllByText("● Baja").length).toBeGreaterThanOrEqual(1);
		// El server ya sirvió sort=risk: la tabla respeta ese orden.
		const rows = screen.getAllByRole("row");
		expect(rows[1]?.textContent).toContain("#101");
		expect(rows[2]?.textContent).toContain("#102");
		expect(rows[3]?.textContent).toContain("#103");
		expect(rows[4]?.textContent).toContain("#104");
		// Sin score → "—" en la celda de riesgo (el otro "—" es su conteo).
		expect(rows[4]?.textContent).not.toContain("Alta");
	});

	it("filtro de severidad deja solo PRs con hallazgos de esa severidad", async () => {
		mockApi(prsByRisk, metrics);
		render(<Triage />, { wrapper: createWrapper() });

		await screen.findByText("acme/codeowl#101");
		fireEvent.change(screen.getByLabelText("Severidad"), {
			target: { value: "high" },
		});

		expect(screen.getByText("acme/codeowl#101")).toBeTruthy();
		expect(screen.queryByText("acme/codeowl#102")).toBeNull();
		expect(screen.queryByText("acme/otro-repo#103")).toBeNull();
		expect(screen.queryByText("#104")).toBeNull();
	});

	it("filtro de repo deja solo los PRs de ese repo", async () => {
		mockApi(prsByRisk, metrics);
		render(<Triage />, { wrapper: createWrapper() });

		await screen.findByText("acme/codeowl#101");
		// El select ofrece los repos de los datos traídos.
		fireEvent.change(screen.getByLabelText("Repo"), {
			target: { value: "2" },
		});

		expect(screen.getByText("acme/otro-repo#103")).toBeTruthy();
		expect(screen.queryByText("acme/codeowl#101")).toBeNull();
		expect(screen.queryByText("#104")).toBeNull();
	});

	it("cambiar el estado consulta al server con state=closed", async () => {
		mockApi(prsByRisk, metrics);
		render(<Triage />, { wrapper: createWrapper() });

		await screen.findByText("acme/codeowl#101");
		fireEvent.change(screen.getByLabelText("Estado"), {
			target: { value: "cerrados" },
		});
		await screen.findByText("acme/codeowl#101");

		expect(apiMock).toHaveBeenCalledWith("/api/prs?sort=risk&state=closed");
	});

	it("estado Todos omite el parámetro state", async () => {
		mockApi(prsByRisk, metrics);
		render(<Triage />, { wrapper: createWrapper() });

		await screen.findByText("acme/codeowl#101");
		fireEvent.change(screen.getByLabelText("Estado"), {
			target: { value: "todos" },
		});
		await screen.findByText("acme/codeowl#101");

		expect(apiMock).toHaveBeenCalledWith("/api/prs?sort=risk");
	});

	it("click en la fila navega al detalle del PR", async () => {
		mockApi(prsByRisk, metrics);
		render(<Triage />, { wrapper: createWrapper() });

		const cell = await screen.findByText("acme/codeowl#101");
		fireEvent.click(cell.closest("tr") as HTMLTableRowElement);

		expect(location.hash).toBe("#/prs/101");
	});

	it("filtros client-side sin coincidencias muestran mensaje amigable", async () => {
		mockApi(
			[makePr(), makePr({ id: 102, number: 102, author: "beto" })],
			metrics,
		);
		render(<Triage />, { wrapper: createWrapper() });

		await screen.findByText("acme/codeowl#101");
		fireEvent.change(screen.getByLabelText("Severidad"), {
			target: { value: "high" },
		});

		expect(screen.getByText(/Ningún PR cumple con los filtros/)).toBeTruthy();
		expect(screen.queryByRole("table")).toBeNull();
	});

	it("sin PRs en el server muestra el mensaje de vacío, no una tabla", async () => {
		mockApi([], metrics);
		render(<Triage />, { wrapper: createWrapper() });

		expect(
			await screen.findByText(/Todavía no hay pull requests/),
		).toBeTruthy();
		expect(screen.queryByRole("table")).toBeNull();
	});

	it("error del listado muestra el banner de error", async () => {
		apiMock.mockImplementation((path: string) =>
			path.startsWith("/api/metrics")
				? Promise.resolve(metrics)
				: Promise.reject(new Error("boom")),
		);
		render(<Triage />, { wrapper: createWrapper() });

		expect(
			await screen.findByText("No se pudo cargar la lista de PRs."),
		).toBeTruthy();
	});
});
