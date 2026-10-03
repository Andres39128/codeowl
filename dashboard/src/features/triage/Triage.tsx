/** Triage de PRs por riesgo (guía §6 F5, decisión 9): tabla ordenada por
 * riesgo con badges por bandas del score, filtros de estado (server, T7) y de
 * severidad y repo (client-side — single-org, dataset chico). Arriba, las
 * métricas de outcome y costo de los últimos 30 días como texto: números,
 * sin gráficos. Visible a todo usuario autenticado (§3.4); cada fila navega
 * al detalle #/prs/<id>. */

import { useQuery } from "@tanstack/react-query";
import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import { Badge } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { Card } from "../../components/Card";
import { type Column, Table } from "../../components/Table";
import { api } from "../../lib/apiClient";
import type { MetricsView, PrView } from "../../lib/types";
import { reviewStatusOf } from "../prs/view";
import { riskBandOf } from "./view";

/** Filtro de estado: open y closed viajan al server (T7); "todos" omite el
 * parámetro — la API trata la ausencia como sin filtro. */
type EstadoFiltro = "abiertos" | "cerrados" | "todos";

/** Severidad: filtra client-side sobre los counts de la última review. */
type SeveridadFiltro = "todas" | "high" | "medium" | "low";

const ESTADO_PARAM: Record<EstadoFiltro, string> = {
	abiertos: "&state=open",
	cerrados: "&state=closed",
	todos: "",
};

// Select de filtro: mismo estilo que inputClass (Field) sin w-full/mt-1 —
// los filtros comparten fila en vez de apilarse como en los modals.
const filterClass =
	"rounded-md border border-border-subtle bg-bg-elevated px-3 py-1.5 text-sm";

/** Número es-AR con 1 decimal; null → "—" con title (§9.9: null honesto,
 * sin datos suficientes, jamás 0 disfrazado de dato). */
function metricHours(value: number | null): ComponentChildren {
	if (value === null) return <span title="Sin datos suficientes">—</span>;
	return value.toLocaleString("es-AR", {
		minimumFractionDigits: 1,
		maximumFractionDigits: 1,
	});
}

/** Tasa 0..1 como porcentaje es-AR; null → "—" con title. */
function metricRate(value: number | null): ComponentChildren {
	if (value === null) return <span title="Sin datos suficientes">—</span>;
	return `${(value * 100).toLocaleString("es-AR", { maximumFractionDigits: 1 })}%`;
}

/** Tokens promedio por review, sin decimales; null → "—" con title. */
function metricTokens(value: number | null): ComponentChildren {
	if (value === null) return <span title="Sin datos suficientes">—</span>;
	return value.toLocaleString("es-AR", { maximumFractionDigits: 0 });
}

/** Strip de métricas (§6 F5): cycle time, % aceptados, tasa FP y costo medio.
 * Solo números — la guía no pide dashboard de métricas con gráficos. */
function MetricsCard({ metrics }: { metrics: MetricsView }) {
	return (
		<Card title="Métricas (últimos 30 días)" class="mb-4">
			<dl class="flex flex-wrap gap-x-8 gap-y-2">
				<div>
					<dt class="text-xs text-text-muted">Cycle time medio (h)</dt>
					<dd class="text-lg font-semibold">
						{metricHours(metrics.avg_cycle_time_hours)}
					</dd>
				</div>
				<div>
					<dt class="text-xs text-text-muted">% aceptados</dt>
					<dd class="text-lg font-semibold">
						{metricRate(metrics.accepted_rate)}
					</dd>
				</div>
				<div>
					<dt class="text-xs text-text-muted">Tasa de falsos positivos</dt>
					<dd class="text-lg font-semibold">
						{metricRate(metrics.false_positive_rate)}
					</dd>
				</div>
				<div>
					<dt class="text-xs text-text-muted">Costo medio (tokens/review)</dt>
					<dd class="text-lg font-semibold">
						{metricTokens(metrics.avg_tokens_per_review)}
					</dd>
				</div>
			</dl>
		</Card>
	);
}

/** Conteo de hallazgos por severidad con ceros omitidos; sin corridas o sin
 * hallazgos queda en texto muted (espejo de Prs.tsx: dos consumidores aún —
 * se extrae con el tercero). */
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

function riskCell(pr: PrView): ComponentChildren {
	const band = riskBandOf(pr.risk_score);
	if (band === null) return <span class="text-text-muted">—</span>;
	// String único: ícono + texto en un solo nodo (§5.2.3).
	return <Badge tone={band.tone}>{`● ${band.label}`}</Badge>;
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
		key: "risk",
		header: "Riesgo",
		render: riskCell,
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
						{new Date(pr.latest_review.created_at).toLocaleDateString("es-AR")}
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
	{
		key: "updated_at",
		header: "Actualizado",
		render: (pr) => new Date(pr.updated_at).toLocaleDateString("es-AR"),
	},
];

export function Triage() {
	const [estado, setEstado] = useState<EstadoFiltro>("abiertos");
	const [severidad, setSeveridad] = useState<SeveridadFiltro>("todas");
	const [repo, setRepo] = useState("todos");

	const prs = useQuery({
		queryKey: ["prs", "risk", estado],
		queryFn: () => api<PrView[]>(`/api/prs?sort=risk${ESTADO_PARAM[estado]}`),
	});
	const metrics = useQuery({
		queryKey: ["metrics", 30],
		queryFn: () => api<MetricsView>("/api/metrics?days=30"),
		refetchInterval: 60_000,
	});

	// Filtros de severidad y repo son client-side (decisión 9): el server ya
	// hizo sort=risk y state; acá solo se descartan filas del listado traído.
	const rows = (prs.data ?? []).filter(
		(pr) =>
			(severidad === "todas" ||
				(pr.latest_review !== null &&
					pr.latest_review.counts[severidad] > 0)) &&
			(repo === "todos" || String(pr.repo.id) === repo),
	);

	// Repos distintos de los datos ya traídos: el select nunca ofrece un repo
	// que no está en la lista (decisión 9).
	const repoOptions = new Map<number, string>();
	for (const pr of prs.data ?? []) {
		repoOptions.set(pr.repo.id, `${pr.repo.owner}/${pr.repo.name}`);
	}

	return (
		<section>
			<h1 class="mb-1 text-xl font-semibold">Triage</h1>
			<p class="mb-4 text-sm text-text-muted">
				PRs ordenados por riesgo para decidir qué revisar primero.
			</p>

			{metrics.isError ? (
				<p class="mb-4 text-sm text-text-muted">
					No se pudieron cargar las métricas.
				</p>
			) : metrics.isPending ? (
				<p class="mb-4 text-sm text-text-muted">Cargando métricas…</p>
			) : (
				<MetricsCard metrics={metrics.data} />
			)}

			<div class="mb-4 flex flex-wrap items-end gap-3">
				<div class="flex flex-col gap-1">
					<label class="text-xs text-text-muted" for="triage-estado">
						Estado
					</label>
					<select
						id="triage-estado"
						class={filterClass}
						value={estado}
						// onInput y no onChange: convención de Preact — el change del
						// select se quiebra bajo jsdom (ver ProviderModal).
						onInput={(event) =>
							setEstado(
								(event.target as HTMLSelectElement).value as EstadoFiltro,
							)
						}
					>
						<option value="abiertos">Abiertos</option>
						<option value="cerrados">Cerrados</option>
						<option value="todos">Todos</option>
					</select>
				</div>
				<div class="flex flex-col gap-1">
					<label class="text-xs text-text-muted" for="triage-severidad">
						Severidad
					</label>
					<select
						id="triage-severidad"
						class={filterClass}
						value={severidad}
						onInput={(event) =>
							setSeveridad(
								(event.target as HTMLSelectElement).value as SeveridadFiltro,
							)
						}
					>
						<option value="todas">Todas</option>
						<option value="high">Alta</option>
						<option value="medium">Media</option>
						<option value="low">Baja</option>
					</select>
				</div>
				<div class="flex flex-col gap-1">
					<label class="text-xs text-text-muted" for="triage-repo">
						Repo
					</label>
					<select
						id="triage-repo"
						class={filterClass}
						value={repo}
						onInput={(event) =>
							setRepo((event.target as HTMLSelectElement).value)
						}
					>
						<option value="todos">Todos</option>
						{[...repoOptions].map(([id, label]) => (
							<option key={id} value={String(id)}>
								{label}
							</option>
						))}
					</select>
				</div>
			</div>

			{prs.isError ? (
				<Banner tone="error">No se pudo cargar la lista de PRs.</Banner>
			) : prs.isPending ? (
				<p class="text-text-muted">Cargando…</p>
			) : prs.data.length === 0 ? (
				<Banner tone="info">
					Todavía no hay pull requests para este estado. Aparecen acá cuando los
					repos conectados registran actividad.
				</Banner>
			) : rows.length === 0 ? (
				<Banner tone="info">
					Ningún PR cumple con los filtros de severidad y repo elegidos.
				</Banner>
			) : (
				<Table
					caption="PRs ordenados por riesgo con hallazgos y última review"
					columns={columns}
					rows={rows}
					getRowKey={(pr) => String(pr.id)}
					onRowClick={(pr) => {
						location.hash = `#/prs/${pr.id}`;
					}}
				/>
			)}
		</section>
	);
}
