/** Panel de cola (guía §9.9): pending/running/discarded por tipo de job, con
 * cantidad y última creación. Disponible para todo usuario autenticado (§3.4)
 * y con refresco automático cada 5 segundos. */

import { useQuery } from "@tanstack/react-query";
import { Badge, type BadgeTone } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { type Column, Table } from "../../components/Table";
import { api } from "../../lib/apiClient";
import type { JobRow } from "../../lib/types";

/** Etiqueta y tono por estado. `available` y `pending` son "en espera": River
 * v0.48 inserta los jobs nuevos como available (api/queue.go). */
const stateView: Record<JobRow["state"], { label: string; tone: BadgeTone }> = {
	available: { label: "En espera", tone: "media" },
	pending: { label: "En espera", tone: "media" },
	running: { label: "En ejecución", tone: "baja" },
	discarded: { label: "Descartado", tone: "alta" },
};

function stateOf(job: JobRow): { label: string; tone: BadgeTone } {
	// Estado fuera del conjunto cerrado: texto crudo en neutral, sin crashear.
	return stateView[job.state] ?? { label: job.state, tone: "neutral" };
}

const columns: Column<JobRow>[] = [
	{
		key: "kind",
		header: "Tipo",
		render: (job) => <Badge>{job.kind}</Badge>,
	},
	{
		key: "state",
		header: "Estado",
		render: (job) => {
			const view = stateOf(job);
			// String único: ícono + texto en un solo nodo (§5.2.3).
			return <Badge tone={view.tone}>{`● ${view.label}`}</Badge>;
		},
	},
	{ key: "count", header: "Cantidad" },
	{
		key: "latest_created_at",
		header: "Última creación",
		render: (job) => new Date(job.latest_created_at).toLocaleString("es-AR"),
	},
];

export function Queue() {
	const jobs = useQuery({
		queryKey: ["jobs"],
		queryFn: () => api<JobRow[]>("/api/jobs"),
		refetchInterval: 5000,
	});

	return (
		<section>
			<h1 class="mb-1 text-xl font-semibold">Cola de Jobs</h1>
			<p class="mb-4 text-sm text-text-muted">
				Estado de la cola por tipo de job; se actualiza cada 5 segundos.
			</p>

			{jobs.isError ? (
				<Banner tone="error">No se pudo cargar la cola de jobs.</Banner>
			) : jobs.isPending ? (
				<p class="text-text-muted">Cargando…</p>
			) : (
				<Table
					caption="Jobs agrupados por tipo y estado"
					columns={columns}
					rows={jobs.data}
					getRowKey={(job) => `${job.kind}-${job.state}`}
				/>
			)}
		</section>
	);
}
