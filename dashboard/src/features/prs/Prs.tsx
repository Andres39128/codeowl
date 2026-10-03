/** Listado de PRs (guía §6 F3): número/autor, repo, estado, última corrida y
 * conteo de hallazgos por severidad. Visible a member y admin (§3.4); cada
 * fila navega al detalle #/prs/<id>. */

import { useQuery } from "@tanstack/react-query";
import type { ComponentChildren } from "preact";
import { Badge } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { type Column, Table } from "../../components/Table";
import { api } from "../../lib/apiClient";
import type { PrView } from "../../lib/types";
import { reviewStatusOf, stateOf } from "./view";

/** Conteo de hallazgos por severidad con ceros omitidos; sin corridas o sin
 * hallazgos queda en texto muted. */
function countsOf(pr: PrView): ComponentChildren {
	const review = pr.latest_review;
	if (review === null) return <span class="text-text-muted">—</span>;
	const { high, medium, low } = review.counts;
	const parts: ComponentChildren[] = [];
	if (high > 0) {
		parts.push(<span class="text-severity-alta">{`${high} alta`}</span>);
	}
	if (medium > 0) {
		parts.push(<span class="text-severity-media">{`${medium} media`}</span>);
	}
	if (low > 0) {
		parts.push(<span class="text-severity-baja">{`${low} baja`}</span>);
	}
	if (parts.length === 0) {
		return <span class="text-text-muted">Sin hallazgos</span>;
	}
	return <span class="flex gap-2">{parts}</span>;
}

const columns: Column<PrView>[] = [
	{
		key: "pr",
		header: "PR",
		render: (pr) => (
			<span class="flex flex-col">
				<a
					href={`#/prs/${pr.id}`}
					class="font-medium text-action-primary hover:underline"
				>
					{`#${pr.number}`}
				</a>
				<span class="text-xs text-text-muted">{pr.author}</span>
			</span>
		),
	},
	{
		key: "repo",
		header: "Repo",
		render: (pr) => `${pr.repo.owner}/${pr.repo.name}`,
	},
	{
		key: "state",
		header: "Estado",
		render: (pr) => {
			const view = stateOf(pr.state);
			// String único: ícono + texto en un solo nodo (§5.2.3).
			return <Badge tone={view.tone}>{`● ${view.label}`}</Badge>;
		},
	},
	{
		key: "latest_review",
		header: "Última review",
		render: (pr) => {
			if (pr.latest_review === null) {
				return <span class="text-text-muted">Sin corridas</span>;
			}
			const view = reviewStatusOf(pr.latest_review.status);
			return (
				<span class="flex flex-col">
					<Badge tone={view.tone}>{`● ${view.label}`}</Badge>
					<span class="text-xs text-text-muted">
						{new Date(pr.latest_review.created_at).toLocaleString("es-AR")}
					</span>
				</span>
			);
		},
	},
	{
		key: "counts",
		header: "Hallazgos",
		render: countsOf,
	},
];

export function Prs() {
	const prs = useQuery({
		queryKey: ["prs"],
		queryFn: () => api<PrView[]>("/api/prs"),
	});

	return (
		<section>
			<h1 class="mb-1 text-xl font-semibold">Pull Requests</h1>
			<p class="mb-4 text-sm text-text-muted">
				PRs de los repos conectados con el resultado de su última revisión.
			</p>

			{prs.isError ? (
				<Banner tone="error">No se pudo cargar la lista de PRs.</Banner>
			) : prs.isPending ? (
				<p class="text-text-muted">Cargando…</p>
			) : prs.data.length === 0 ? (
				<Banner tone="info">
					Todavía no hay pull requests. Aparecen acá cuando los repos conectados
					registran actividad.
				</Banner>
			) : (
				<Table
					caption="PRs con estado, última review y hallazgos"
					columns={columns}
					rows={prs.data}
					getRowKey={(pr) => String(pr.id)}
					onRowClick={(pr) => {
						location.hash = `#/prs/${pr.id}`;
					}}
				/>
			)}
		</section>
	);
}
