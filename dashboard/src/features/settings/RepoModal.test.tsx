/** RepoModal: el flujo "Detectar ID con la App" completa el external_id sin
 * salir del dashboard — el requisito para conectar repos privados (§3.3). */

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../lib/apiClient";
import { createWrapper } from "../../test/createWrapper";
import { RepoModal } from "./RepoModal";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));
vi.mock("../../lib/apiClient", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../../lib/apiClient")>();
	return { ...actual, api: apiMock };
});

function renderModal() {
	return render(<RepoModal onClose={() => {}} />, { wrapper: createWrapper() });
}

function completarOwnerYNombre() {
	fireEvent.input(screen.getByLabelText("Owner"), {
		target: { value: "acme" },
	});
	fireEvent.input(screen.getByLabelText("Nombre del repo"), {
		target: { value: "privado" },
	});
}

beforeEach(() => {
	apiMock.mockReset();
});

afterEach(cleanup);

describe("RepoModal — detectar ID con la App", () => {
	it("completa el external_id y confirma la instalación", async () => {
		apiMock.mockResolvedValue({
			installed: true,
			external_id: 1398958392,
			full_name: "acme/privado",
		});
		renderModal();
		completarOwnerYNombre();

		fireEvent.click(screen.getByRole("button", { name: /detectar id/i }));

		await waitFor(() => {
			expect(
				(screen.getByLabelText(/ID externo/) as HTMLInputElement).value,
			).toBe("1398958392");
		});
		expect(screen.getByText(/App instalada ✓/)).toBeTruthy();
		// La conexión consulta al endpoint nuevo con owner y name codificados.
		expect(apiMock).toHaveBeenCalledWith(
			"/api/repos/github/resolve?owner=acme&name=privado",
		);
	});

	it("muestra el remedio cuando la App no tiene acceso", async () => {
		apiMock.mockRejectedValue(
			new ApiError(
				404,
				"la GitHub App no tiene acceso a acme/privado: instalala y volvé a intentar",
			),
		);
		renderModal();
		completarOwnerYNombre();

		fireEvent.click(screen.getByRole("button", { name: /detectar id/i }));

		await waitFor(() => {
			expect(screen.getByText(/instalala/i)).toBeTruthy();
		});
	});
});
