/** Listado de pushes/merges directos a la rama base (trazabilidad-merges S1):
 * cada push se analizó con el pipeline completo igual que un PR, pero no hay
 * PR donde publicar — el resultado vive solo en el dashboard (S2). Visible a
 * member y admin (§3.4); cada fila navega al detalle #/trazabilidad/<id>. */

import { useQuery } from "@tanstack/react-query";
import type { ComponentChildren } from "preact";
import { Badge } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { type Column, Table } from "../../components/Table";
import { api } from "../../lib/apiClient";
import type { PushListItem } from "../../lib/types";
import { reviewStatusOf } from "../prs/view";

/** Conteo de hallazgos por severidad con ceros omitidos (espejo del listado
 * de PRs); sin corridas queda el guion muted. */
function countsOf(push: PushListItem): ComponentChildren {
	if (push.latest_review === null)
		return <span class="text-text-muted">—</span>;
	const { high, medium, low } = push.findings_count;
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

const columns: Column<PushListItem>[] = [
	{
		key: "repo",
		header: "Repo",
		render: (push) => (
			<span class="flex flex-col">
				<a
					href={`#/trazabilidad/${push.id}`}
					class="font-medium text-action-primary hover:underline"
					title={`${push.repo.owner}/${push.repo.name}@${push.branch}`}
				>
					{`${push.repo.owner}/${push.repo.name}`}
				</a>
				<span class="font-mono text-xs text-text-muted">{push.branch}</span>
			</span>
		),
	},
	{
		key: "committer",
		header: "Committer",
		render: (push) => push.committer,
	},
	{
		key: "shas",
		header: "Merge (before → after)",
		render: (push) => (
			// SHAs completos en el title: los 7 chars son solo display, la
			// identidad del merge es el par completo (internal/api/pushes.go).
			<span
				class="font-mono text-xs"
				title={`${push.before_sha} → ${push.after_sha}`}
			>
				{`${push.before_sha.slice(0, 7)} → ${push.after_sha.slice(0, 7)}`}
			</span>
		),
	},
	{
		key: "created_at",
		header: "Fecha",
		render: (push) => (
			<span class="text-xs text-text-muted">
				{new Date(push.created_at).toLocaleString("es-AR")}
			</span>
		),
	},
	{
		key: "latest_review",
		header: "Última corrida",
		render: (push) => {
			if (push.latest_review === null) {
				return <span class="text-text-muted">Sin corridas</span>;
			}
			const view = reviewStatusOf(push.latest_review.status);
			return (
				<span class="flex flex-col">
					<Badge tone={view.tone}>{`● ${view.label}`}</Badge>
					<span class="text-xs text-text-muted">
						{new Date(push.latest_review.created_at).toLocaleString("es-AR")}
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

export function Trazabilidad() {
	const pushes = useQuery({
		queryKey: ["pushes"],
		queryFn: () => api<PushListItem[]>("/api/pushes"),
	});

	return (
		<section>
			<h1 class="mb-1 text-xl font-semibold">Trazabilidad</h1>
			<p class="mb-4 text-sm text-text-muted">
				Merges y pushes directos a la rama base, analizados igual que un PR.
			</p>

			{pushes.isError ? (
				<Banner tone="error">No se pudo cargar la lista de pushes.</Banner>
			) : pushes.isPending ? (
				<p class="text-text-muted">Cargando…</p>
			) : pushes.data.length === 0 ? (
				<Banner tone="info">
					Todavía no hay pushes directos. Aparecen acá cuando alguien pushea a
					la rama base de un repo conectado.
				</Banner>
			) : (
				<Table
					caption="Pushes directos con estado de corrida y hallazgos"
					columns={columns}
					rows={pushes.data}
					getRowKey={(push) => String(push.id)}
					onRowClick={(push) => {
						location.hash = `#/trazabilidad/${push.id}`;
					}}
				/>
			)}
		</section>
	);
}
