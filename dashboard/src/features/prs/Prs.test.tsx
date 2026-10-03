/** Prs: filas del listado, conteos por severidad, vacío y navegación a la
 * fila del detalle (mapa: dashboard.features.prs). */

import { cleanup, fireEvent, render, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PrView } from "../../lib/types";
import { createWrapper } from "../../test/createWrapper";
import { Prs } from "./Prs";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));

vi.mock("../../lib/apiClient", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../lib/apiClient")>();
	return { ...actual, api: apiMock };
});

function makePr(overrides: Partial<PrView> = {}): PrView {
	return {
		id: 42,
		number: 42,
		author: "ana",
		state: "open",
		repo: { id: 1, owner: "acme", name: "codeowl", vcs: "github" },
		head_sha: "abc1234abcdef",
		base_ref: "main",
		updated_at: "2026-10-01T12:00:00Z",
		latest_review: {
			id: 1,
			status: "success",
			created_at: "2026-10-01T12:00:00Z",
			counts: { high: 2, medium: 0, low: 1 },
		},
		...overrides,
	};
}

beforeEach(() => {
	apiMock.mockReset();
	location.hash = "";
});

afterEach(cleanup);

describe("Prs", () => {
	it("lista los PRs con estado, review y conteos de severidad", async () => {
		apiMock.mockResolvedValue([
			makePr(),
			makePr({
				id: 43,
				number: 43,
				author: "beto",
				state: "closed",
				repo: { id: 2, owner: "acme", name: "otro-repo", vcs: "gitlab" },
				latest_review: null,
			}),
		]);
		render(<Prs />, { wrapper: createWrapper() });

		expect(await screen.findByText("#42")).toBeTruthy();
		expect(screen.getByText("ana")).toBeTruthy();
		expect(screen.getByText("acme/codeowl")).toBeTruthy();
		expect(screen.getByText(/● Abierto/)).toBeTruthy();
		expect(screen.getByText(/● Cerrado/)).toBeTruthy();
		expect(screen.getByText(/● Exitosa/)).toBeTruthy();
		// Conteos: ceros omitidos, sin corridas queda el guion muted.
		expect(screen.getByText("2 alta")).toBeTruthy();
		expect(screen.getByText("1 baja")).toBeTruthy();
		expect(screen.queryByText("0 media")).toBeNull();
		expect(screen.getByText("—")).toBeTruthy();
	});

	it("lista vacía muestra mensaje amigable, no una tabla vacía", async () => {
		apiMock.mockResolvedValue([]);
		render(<Prs />, { wrapper: createWrapper() });

		expect(
			await screen.findByText(/Todavía no hay pull requests/),
		).toBeTruthy();
		expect(screen.queryByRole("table")).toBeNull();
	});

	it("error de la API muestra el banner de error", async () => {
		apiMock.mockRejectedValue(new Error("boom"));
		render(<Prs />, { wrapper: createWrapper() });

		expect(
			await screen.findByText("No se pudo cargar la lista de PRs."),
		).toBeTruthy();
	});

	it("click en la fila navega al detalle del PR", async () => {
		apiMock.mockResolvedValue([makePr()]);
		render(<Prs />, { wrapper: createWrapper() });

		const cell = await screen.findByText("#42");
		fireEvent.click(cell.closest("tr") as HTMLTableRowElement);

		expect(location.hash).toBe("#/prs/42");
	});
});
