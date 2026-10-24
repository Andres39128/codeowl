/** Panel de cola (guía §9.9): estado vivo por tipo de job y, en la pestaña
 * historial, los últimos jobs finalizados con duración y error. Disponible
 * para todo usuario autenticado (§3.4) y con refresco automático cada 5
 * segundos. */

import { useQuery } from "@tanstack/react-query";
import { useState } from "preact/hooks";
import { Badge, type BadgeTone } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { type Column, Table } from "../../components/Table";
import { api } from "../../lib/apiClient";
import type { JobHistoryRow, JobRow } from "../../lib/types";

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

/** Estados finales de la historia (conjunto cerrado de river_job). */
const historyView: Record<
	JobHistoryRow["state"],
	{ label: string; tone: BadgeTone }
> = {
	completed: { label: "Completado", tone: "baja" },
	cancelled: { label: "Cancelado", tone: "neutral" },
	discarded: { label: "Descartado", tone: "alta" },
};

function historyStateOf(row: JobHistoryRow): { label: string; tone: BadgeTone } {
	return historyView[row.state] ?? { label: row.state, tone: "neutral" };
}

/** Duración legible (ms → "2,5s" / "1m 30s" / "12,4m"): el dato crudo viaja
 * en el title. */
function duracion(ms: number): string {
	if (ms < 1000) return `${ms} ms`;
	const seg = ms / 1000;
	if (seg < 60) return `${seg.toLocaleString("es-AR", { maximumFractionDigits: 1 })}s`;
	const min = Math.floor(seg / 60);
	if (min < 60) return `${min}m ${Math.round(seg - min * 60)}s`;
	return `${(min / 60).toLocaleString("es-AR", { maximumFractionDigits: 1 })}h`;
}

const historyColumns: Column<JobHistoryRow>[] = [
	{
		key: "kind",
		header: "Tipo",
		render: (row) => <Badge>{row.kind}</Badge>,
	},
	{
		key: "state",
		header: "Resultado",
		render: (row) => {
			const view = historyStateOf(row);
			return <Badge tone={view.tone}>{`● ${view.label}`}</Badge>;
		},
	},
	{
		key: "duration_ms",
		header: "Duración",
		render: (row) => (
			<span title={`${row.duration_ms} ms`}>{duracion(row.duration_ms)}</span>
		),
	},
	{
		key: "finalized_at",
		header: "Finalizado",
		render: (row) => new Date(row.finalized_at).toLocaleString("es-AR"),
	},
	{
		key: "error",
		header: "Error",
		render: (row) =>
			row.error === null ? (
				<span class="text-text-muted">—</span>
			) : (
				<code
					class="block max-w-96 truncate font-mono text-xs text-severity-alta"
					title={row.error}
				>
					{row.error}
				</code>
			),
	},
];

export function Queue() {
	// La pestaña activa decide qué consulta corre: una sola cosa por vez.
	const [pestana, setPestana] = useState<"cola" | "historial">("cola");
	const jobs = useQuery({
		queryKey: ["jobs"],
		queryFn: () => api<JobRow[]>("/api/jobs"),
		refetchInterval: 5000,
	});
	const historia = useQuery({
		queryKey: ["jobs-history"],
		queryFn: () => api<JobHistoryRow[]>("/api/jobs/history"),
		refetchInterval: 5000,
		enabled: pestana === "historial",
	});

	const pestañas: { id: "cola" | "historial"; label: string }[] = [
		{ id: "cola", label: "En cola" },
		{ id: "historial", label: "Historial" },
	];

	return (
		<section>
			<h1 class="mb-1 text-xl font-semibold">Cola de Jobs</h1>
			<p class="mb-4 text-sm text-text-muted">
				Estado de la cola por tipo de job; se actualiza cada 5 segundos.
			</p>

			<div class="mb-4 flex gap-1" role="tablist">
				{pestañas.map((p) => (
					<button
						key={p.id}
						type="button"
						role="tab"
						aria-selected={pestana === p.id}
						class={`rounded-md px-3 py-1.5 text-sm ${
							pestana === p.id
								? "bg-bg-elevated font-medium text-text-primary"
								: "text-text-muted hover:text-text-primary"
						}`}
						onClick={() => setPestana(p.id)}
					>
						{p.label}
					</button>
				))}
			</div>

			{pestana === "cola" ? (
				jobs.isError ? (
					<Banner tone="error">No se pudo cargar la cola de jobs.</Banner>
				) : jobs.isPending ? (
					<p class="text-text-muted">Cargando…</p>
				) : jobs.data.length === 0 ? (
					// Sin jobs: no hay tabla vacía que interpretar — se dice qué
					// significa y qué esperaría ver acá.
					<Banner tone="info">
						No hay jobs en la cola: no hay revisión, indexación ni métricas
						pendientes ni en ejecución. Cuando se dispare una corrida (por
						webhook o reconexión) el job aparece acá hasta que el worker lo
						termina.
					</Banner>
				) : (
					<Table
						caption="Jobs agrupados por tipo y estado"
						columns={columns}
						rows={jobs.data}
						getRowKey={(job) => `${job.kind}-${job.state}`}
					/>
				)
			) : historia.isError ? (
				<Banner tone="error">No se pudo cargar el historial de jobs.</Banner>
			) : historia.isPending ? (
				<p class="text-text-muted">Cargando…</p>
			) : historia.data.length === 0 ? (
				<Banner tone="info">
					Todavía no hay jobs terminados: acá aparecen los últimos 50 con su
					resultado (completado, cancelado o descartado), cuánto tardaron y el
					error si fallaron.
				</Banner>
			) : (
				<Table
					caption="Últimos 50 jobs finalizados, lo más reciente primero"
					columns={historyColumns}
					rows={historia.data}
					getRowKey={(row) => `${row.kind}-${row.finalized_at}`}
				/>
			)}
		</section>
	);
}
