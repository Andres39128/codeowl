/** usePR sobre apiClient mockeado: detalle + diff en paralelo, diff
 * degradable y 404 del detalle (mapa: dashboard.lib — F3). */

import { renderHook, waitFor } from "@testing-library/preact";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { createWrapper } from "../test/createWrapper";
import { ApiError } from "./apiClient";
import type { PrDetailView } from "./types";
import { usePR } from "./usePR";

const { apiMock } = vi.hoisted(() => ({ apiMock: vi.fn() }));

vi.mock("./apiClient", async (importOriginal) => {
	const actual = await importOriginal<typeof import("./apiClient")>();
	return { ...actual, api: apiMock };
});

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
		latest_review: null,
	},
	review: null,
	findings: [],
};

beforeEach(() => {
	apiMock.mockReset();
});

describe("usePR", () => {
	it("trae detalle y diff en paralelo con claves por id", async () => {
		apiMock.mockImplementation((path: string) => {
			if (path === "/api/prs/7") return Promise.resolve(detail);
			if (path === "/api/prs/7/diff") return Promise.resolve({ diff: "diff" });
			return Promise.reject(new Error(`path inesperado: ${path}`));
		});

		const { result } = renderHook(() => usePR(7), {
			wrapper: createWrapper(),
		});

		await waitFor(() => expect(result.current.isLoading).toBe(false));
		expect(result.current.pr).toEqual(detail.pr);
		expect(result.current.review).toBeNull();
		expect(result.current.findings).toEqual([]);
		expect(result.current.diff).toBe("diff");
		expect(result.current.error).toBeNull();
		expect(apiMock).toHaveBeenCalledWith("/api/prs/7");
		expect(apiMock).toHaveBeenCalledWith("/api/prs/7/diff");
	});

	it("404 del detalle queda en error y sin PR", async () => {
		apiMock.mockRejectedValue(new ApiError(404, "PR inexistente"));

		const { result } = renderHook(() => usePR(7), {
			wrapper: createWrapper(),
		});

		await waitFor(() => expect(result.current.isLoading).toBe(false));
		expect(result.current.pr).toBeNull();
		expect(result.current.error).toBeInstanceOf(ApiError);
		expect((result.current.error as ApiError).status).toBe(404);
	});

	it("error del diff (502) es degradable: el detalle sigue disponible", async () => {
		apiMock.mockImplementation((path: string) => {
			if (path === "/api/prs/7") return Promise.resolve(detail);
			return Promise.reject(new ApiError(502, "el VCS no entregó el diff"));
		});

		const { result } = renderHook(() => usePR(7), {
			wrapper: createWrapper(),
		});

		await waitFor(() => expect(result.current.isLoading).toBe(false));
		expect(result.current.pr).toEqual(detail.pr);
		expect(result.current.diff).toBeNull();
		expect(result.current.diffError).toBeInstanceOf(ApiError);
		expect((result.current.diffError as ApiError).status).toBe(502);
	});
});
