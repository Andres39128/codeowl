/** Hook del detalle de PR (mapa: dashboard.lib — F3): trae el detalle y el
 * diff en paralelo con claves ["pr", id] y ["pr", id, "diff"]. Sin reintentos:
 * un 404 (PR inexistente) o un 502 del VCS no se cura con un retry inmediato.
 * El diff es degradable por diseño — su error viaja aparte en `diffError` y no
 * tumba la página del detalle (§6 F3). */

import { useQuery } from "@tanstack/react-query";
import { api } from "./apiClient";
import type {
	FindingView,
	PrDetailView,
	PrDiffView,
	PrView,
	ReviewView,
} from "./types";

export interface PrDetailState {
	pr: PrView | null;
	review: ReviewView | null;
	findings: FindingView[];
	diff: string | null;
	isLoading: boolean;
	error: unknown;
	diffError: unknown;
}

export function usePR(prId: number): PrDetailState {
	const detail = useQuery({
		queryKey: ["pr", prId],
		queryFn: () => api<PrDetailView>(`/api/prs/${prId}`),
		retry: false,
	});

	const diff = useQuery({
		queryKey: ["pr", prId, "diff"],
		queryFn: () => api<PrDiffView>(`/api/prs/${prId}/diff`),
		retry: false,
	});

	return {
		pr: detail.data?.pr ?? null,
		review: detail.data?.review ?? null,
		findings: detail.data?.findings ?? [],
		diff: diff.data?.diff ?? null,
		isLoading: detail.isPending,
		error: detail.error,
		diffError: diff.error,
	};
}
