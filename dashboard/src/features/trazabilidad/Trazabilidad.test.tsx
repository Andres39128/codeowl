/** Trazabilidad: listado de pushes directos con shas cortos, estado de la
 * corrida y conteos de severidad; navegación al detalle y link de nav
 * (mapa: dashboard.features — trazabilidad-merges S1). */

import { cleanup, fireEvent, render, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Layout } from "../../components/Layout";
import { pushIdFromHash, resolveRoute } from "../../lib/router";
import type { PushListItem } from "../../lib/types";
import { createWrapper } from "../../test/createWrapper";
import { Trazabilidad } from "./Trazabilidad";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));

vi.mock("../../lib/apiClient", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../lib/apiClient")>();
	return { ...actual, api: apiMock };
});

function makePush(overrides: Partial<PushListItem> = {}): PushListItem {
	return {
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
		findings_count: { high: 2, medium: 0, low: 1 },
		...overrides,
	};
}

beforeEach(() => {
	apiMock.mockReset();
	location.hash = "";
});

afterEach(cleanup);

describe("Trazabilidad", () => {
	it("lista los pushes con shas cortos, estado de corrida y conteos", async () => {
		apiMock.mockResolvedValue([
			makePush(),
			makePush({
				id: 4,
				repo: { id: 2, owner: "acme", name: "otro-repo", vcs: "gitlab" },
				branch: "master",
				committer: "beto",
				before_sha: "9999999ccccccc",
				after_sha: "8888888ddddddd",
				latest_review: null,
			}),
		]);
		render(<Trazabilidad />, { wrapper: createWrapper() });

		expect(await screen.findByText("acme/codeowl")).toBeTruthy();
		expect(screen.getByText("main")).toBeTruthy();
		expect(screen.getByText("ana")).toBeTruthy();
		expect(screen.getByText("beto")).toBeTruthy();
		// SHAs cortos (7 chars) con el par completo a mano en el title.
		const shas = screen.getByText("1111111 → 2222222");
		expect(shas.getAttribute("title")).toContain("1111111aaaaaaa");
		expect(shas.getAttribute("title")).toContain("2222222bbbbbbb");
		// Estado de la corrida: badge de la última; sin corridas, texto muted.
		expect(screen.getByText(/● Exitosa/)).toBeTruthy();
		expect(screen.getByText("Sin corridas")).toBeTruthy();
		// Conteos: ceros omitidos; sin corridas queda el guion muted.
		expect(screen.getByText("2 alta")).toBeTruthy();
		expect(screen.getByText("1 baja")).toBeTruthy();
		expect(screen.queryByText("0 media")).toBeNull();
		expect(screen.getByText("—")).toBeTruthy();
	});

	it("click en la fila navega al detalle del push", async () => {
		apiMock.mockResolvedValue([makePush()]);
		render(<Trazabilidad />, { wrapper: createWrapper() });

		const link = await screen.findByText("acme/codeowl");
		expect(link.getAttribute("href")).toBe("#/trazabilidad/3");

		fireEvent.click(link.closest("tr") as HTMLTableRowElement);
		expect(location.hash).toBe("#/trazabilidad/3");
	});

	it("lista vacía muestra mensaje amigable, no una tabla vacía", async () => {
		apiMock.mockResolvedValue([]);
		render(<Trazabilidad />, { wrapper: createWrapper() });

		expect(await screen.findByText(/Todavía no hay pushes/)).toBeTruthy();
		expect(screen.queryByRole("table")).toBeNull();
	});

	it("error de la API muestra el banner de error", async () => {
		apiMock.mockRejectedValue(new Error("boom"));
		render(<Trazabilidad />, { wrapper: createWrapper() });

		expect(
			await screen.findByText("No se pudo cargar la lista de pushes."),
		).toBeTruthy();
	});
});

// Nav y rutas de trazabilidad: el link vive en Layout y la resolución en
// lib/router — testeados desde acá porque la vista es la dueña del feature.
describe("navegación de trazabilidad", () => {
	it("el Layout tiene el link Trazabilidad activo en su ruta", () => {
		render(
			<Layout
				user={{
					username: "ana",
					role: "member",
					must_change_password: false,
				}}
				route="trazabilidad"
				onLogout={() => {}}
				logoutPending={false}
			>
				<p>contenido</p>
			</Layout>,
		);

		const link = screen.getByRole("link", { name: "Trazabilidad" });
		expect(link.getAttribute("href")).toBe("#/trazabilidad");
		expect(link.getAttribute("aria-current")).toBe("page");
	});

	it("trazabilidad es visible a member y admin sin redirect (§3.4)", () => {
		expect(resolveRoute("#/trazabilidad", false)).toEqual({
			route: "trazabilidad",
			redirect: null,
		});
		expect(resolveRoute("#/trazabilidad", true)).toEqual({
			route: "trazabilidad",
			redirect: null,
		});
	});

	it("detalle #/trazabilidad/<id> comparte la ruta; id inválido vuelve a la lista", () => {
		expect(pushIdFromHash("#/trazabilidad/3")).toBe(3);
		expect(pushIdFromHash("#/trazabilidad")).toBeNull();
		expect(pushIdFromHash("#/trazabilidad/abc")).toBeNull();
		expect(pushIdFromHash("#/trazabilidad/3/extra")).toBeNull();
		expect(resolveRoute("#/trazabilidad/3", false)).toEqual({
			route: "trazabilidad",
			redirect: null,
		});
		expect(resolveRoute("#/trazabilidad/abc", true)).toEqual({
			route: "trazabilidad",
			redirect: "#/trazabilidad",
		});
	});
});
