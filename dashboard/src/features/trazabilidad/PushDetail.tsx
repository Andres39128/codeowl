/** Detalle de un push directo (trazabilidad-merges S1): header, resumen de la
 * corrida, hallazgos y diff — el mismo análisis que un PR pero sin PR donde
 * publicar, todo vive en el dashboard (S2). El mermaid viaja como TEXTO
 * (§9.5) y el diff es degradable: si el VCS falla (502), el resto de la
 * página sigue. */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef } from "preact/hooks";
import { Badge } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { Card } from "../../components/Card";
import { DiffViewer } from "../../components/DiffViewer";
import { ApiError, api } from "../../lib/apiClient";
import type {
	FindingView,
	PushDetailView,
	PushDiffView,
} from "../../lib/types";
import {
	categoryHintOf,
	categoryOf,
	reviewStatusOf,
	severityOf,
	sourceOf,
} from "../prs/view";

/** Un hallazgo del push: versión reducida del HallazgoItem del detalle de PR
 * (que no es exportable) — badges, cuerpo, sugerencia y el botón "No aplica"
 * que registra la huella para las corridas futuras (S4). El dismissed viaja
 * igual (auditoría) y se atenúa. */
function HallazgoItem({
	finding,
	onScroll,
}: {
	finding: FindingView;
	onScroll: (file: string, line: number) => void;
}) {
	const queryClient = useQueryClient();
	const dismiss = useMutation({
		mutationFn: () =>
			api(`/api/findings/${finding.id}/dismiss`, {
				method: "POST",
				body: { reason: "" },
			}),
		// Prefijo ["push"]: refresca el detalle y su diff juntos.
		onSuccess: () => queryClient.invalidateQueries({ queryKey: ["push"] }),
	});
	const sev = severityOf(finding.severity);
	return (
		<li
			class={`rounded-md border border-border-subtle p-3 ${finding.dismissed ? "opacity-50" : ""}`}
		>
			<div class="mb-1 flex flex-wrap items-center gap-2">
				<Badge
					tone={sev.tone}
					title="Severidad del hallazgo: Alta rompe la corrección, Media la debería, Baja es ajuste menor."
				>
					{`● ${sev.label}`}
				</Badge>
				<Badge title={categoryHintOf(finding.category)}>
					{categoryOf(finding.category)}
				</Badge>
				<Badge
					title={
						finding.source === "sast"
							? "Lo detectó un linter determinista (ESLint, Ruff, golangci-lint, Clippy, Gitleaks)."
							: "Lo detectó el modelo de lenguaje (agente Reviewer)."
					}
				>
					{sourceOf(finding.source)}
				</Badge>
				{finding.dismissed && (
					<span
						class="text-xs text-text-muted"
						title="Alguien marcó esta huella como no aplica: queda para auditoría y no vuelve a molestar."
					>
						✕ No aplica
					</span>
				)}
				<button
					type="button"
					class="max-w-full break-all text-left font-mono text-xs text-action-primary underline"
					onClick={() => onScroll(finding.file, finding.line)}
				>
					{`${finding.file}:${finding.line}`}
				</button>
				{!finding.dismissed && (
					<button
						type="button"
						disabled={dismiss.isPending}
						class="ml-auto rounded border border-border-subtle px-2 py-0.5 text-xs text-text-muted hover:bg-bg-elevated disabled:opacity-50"
						title="Marca la huella (archivo + categoría) como no aplica: se oculta acá y en las corridas futuras de este repo. Queda guardada para auditoría."
						onClick={() => dismiss.mutate()}
					>
						{dismiss.isPending ? "Marcando…" : "No aplica"}
					</button>
				)}
			</div>
			<p class="text-sm break-words whitespace-pre-wrap">{finding.body}</p>
			{finding.suggestion !== null && finding.suggestion !== "" && (
				<div class="mt-2">
					<h3 class="mb-1 text-xs font-semibold">Sugerencia</h3>
					<pre class="overflow-auto rounded-md border border-border-subtle bg-bg-elevated p-3 text-xs">
						<code>{finding.suggestion}</code>
					</pre>
				</div>
			)}
		</li>
	);
}

export function PushDetail({ pushId }: { pushId: number }) {
	// Detalle y diff en paralelo con claves hermanas (patrón de usePR); sin
	// reintentos: un 404 o un 502 del VCS no se cura con un retry inmediato.
	const detail = useQuery({
		queryKey: ["push", pushId],
		queryFn: () => api<PushDetailView>(`/api/pushes/${pushId}`),
		retry: false,
	});
	const diffQuery = useQuery({
		queryKey: ["push", pushId, "diff"],
		queryFn: () => api<PushDiffView>(`/api/pushes/${pushId}/diff`),
		retry: false,
	});
	// Contenedor del diff: las filas ancla [data-file][data-line] viven acá.
	const diffContainer = useRef<HTMLDivElement>(null);

	if (detail.isPending) {
		return <p class="text-text-muted">Cargando…</p>;
	}
	if (detail.isError) {
		const notFound =
			detail.error instanceof ApiError && detail.error.status === 404;
		return (
			<Banner tone="error">
				{notFound
					? "Push no encontrado."
					: "No se pudo cargar el detalle del push."}
			</Banner>
		);
	}

	const { push, review, findings } = detail.data;
	const diff = diffQuery.data?.diff ?? null;
	const reviewStatus = review !== null ? reviewStatusOf(review.status) : null;

	/** Lleva el foco a la fila del diff que ancla el hallazgo. */
	const scrollToFinding = (file: string, line: number) => {
		diffContainer.current
			?.querySelector(`[data-file="${CSS.escape(file)}"][data-line="${line}"]`)
			?.scrollIntoView({ block: "center" });
	};

	return (
		<section class="min-w-0">
			<a
				href="#/trazabilidad"
				class="text-sm text-text-muted hover:text-text-primary"
			>
				← Volver a trazabilidad
			</a>

			<Card
				class="mt-3"
				title={`Merge directo · ${push.repo.owner}/${push.repo.name}`}
			>
				<p
					class="font-mono text-sm text-text-muted"
					title={`${push.before_sha} → ${push.after_sha}`}
				>
					{`${push.committer} · ${push.branch} · ${push.before_sha.slice(0, 7)} → ${push.after_sha.slice(0, 7)}`}
				</p>
			</Card>

			<Card title="Resumen" class="mt-3">
				{review === null || reviewStatus === null ? (
					<Banner tone="info">
						Este push aún no tiene corridas de revisión.
					</Banner>
				) : (
					<>
						<div class="mb-2 flex items-center gap-2">
							<Badge
								tone={reviewStatus.tone}
							>{`● ${reviewStatus.label}`}</Badge>
							<span class="text-xs text-text-muted">
								{new Date(review.created_at).toLocaleString("es-AR")}
							</span>
						</div>
						<p class="text-sm whitespace-pre-wrap">{review.summary}</p>
						{review.walkthrough !== "" && (
							<>
								<h3 class="mt-3 mb-1 text-sm font-semibold">Walkthrough</h3>
								<p class="text-sm whitespace-pre-wrap">{review.walkthrough}</p>
							</>
						)}
						{review.mermaid !== "" && (
							<>
								<h3 class="mt-3 mb-1 text-sm font-semibold">
									Diagrama (Mermaid)
								</h3>
								{/* Fuente mermaid como TEXTO (§9.5): sin render del lado
								 * cliente, el escaping lo hace Preact. */}
								<pre class="overflow-auto rounded-md border border-border-subtle bg-bg-elevated p-3 text-xs">
									<code>{review.mermaid}</code>
								</pre>
							</>
						)}
					</>
				)}
			</Card>

			{/* El diff va ANTES de los hallazgos: el revisor lee el código
			 * primero y los hallazgos son el comentario sobre ese código
			 * (§5.3). */}
			<Card title="Diff" class="mt-3">
				{diffQuery.isError ? (
					<Banner tone="warning">No se pudo obtener el diff del VCS.</Banner>
				) : diff === null ? (
					<p class="text-sm text-text-muted">Cargando diff…</p>
				) : (
					<div ref={diffContainer} class="min-w-0 max-w-full">
						<DiffViewer
							diff={diff}
							highlights={findings.map((f) => ({
								file: f.file,
								line: f.line,
							}))}
						/>
					</div>
				)}
			</Card>

			<Card title={`Hallazgos (${findings.length})`} class="mt-3">
				{findings.length === 0 ? (
					<p class="text-sm text-text-muted">Sin hallazgos registrados.</p>
				) : (
					<ul class="flex flex-col gap-3">
						{findings.map((finding) => (
							<HallazgoItem
								key={finding.id}
								finding={finding}
								onScroll={scrollToFinding}
							/>
						))}
					</ul>
				)}
			</Card>
		</section>
	);
}
