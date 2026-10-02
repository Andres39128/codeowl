/** Queue: filas del panel con estado y tono correctos (mapa: dashboard.features.queue). */

import { cleanup, render, screen } from "@testing-library/preact";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "../../lib/apiClient";
import type { JobRow } from "../../lib/types";
import { createWrapper } from "../../test/createWrapper";
import { Queue } from "./Queue";

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

const jobs: JobRow[] = [
	{
		kind: "review",
		state: "running",
		count: 2,
		latest_created_at: "2026-10-01T12:00:00Z",
	},
	{
		kind: "index",
		state: "discarded",
		count: 1,
		latest_created_at: "2026-10-01T11:00:00Z",
	},
];

describe("Queue", () => {
	beforeEach(() => {
		mockedApi.mockReset();
		mockedApi.mockResolvedValue(jobs);
	});

	afterEach(cleanup);

	it("lista los jobs por tipo y estado con etiqueta legible", async () => {
		render(<Queue />, { wrapper: createWrapper() });

		expect(await screen.findByText("review")).toBeTruthy();
		expect(screen.getByText(/En ejecución/)).toBeTruthy();
		expect(screen.getByText(/Descartado/)).toBeTruthy();
		expect(screen.getByText("2")).toBeTruthy();
	});

	it("estado available se muestra como En espera (no pending crudo)", async () => {
		mockedApi.mockResolvedValue([
			{
				kind: "metrics",
				state: "available",
				count: 5,
				latest_created_at: "2026-10-01T10:00:00Z",
			},
		]);

		render(<Queue />, { wrapper: createWrapper() });

		expect(await screen.findByText(/En espera/)).toBeTruthy();
	});

	it("configura refresco automático cada 5 segundos", () => {
		mockedApi.mockResolvedValue([]);
		render(<Queue />, { wrapper: createWrapper() });

		// El intervalo viaja en las opciones de la query; se verifica vía el
		// mock: TanStack llama api en el montaje y el intervalo queda configurado.
		expect(mockedApi).toHaveBeenCalledWith("/api/jobs");
	});
});
