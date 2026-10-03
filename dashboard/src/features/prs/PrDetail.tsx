/** Detalle de PR (guía §6 F3): header, resumen de la corrida, findings con
 * estado de verificación y diff con anclas por hallazgo. Todo contenido del
 * VCS o del LLM se renderiza como TEXTO — el mermaid nunca se dibuja del lado
 * cliente y el escaping es el nativo de Preact, jamás innerHTML (§9.5). El
 * diff es degradable: si el VCS falla (502), el resto de la página sigue. */

import { useRef } from "preact/hooks";
import { Badge } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { Card } from "../../components/Card";
import { DiffViewer } from "../../components/DiffViewer";
import { ApiError } from "../../lib/apiClient";
import type { FindingView } from "../../lib/types";
import { usePR } from "../../lib/usePR";
import { reviewStatusOf, severityOf, sourceOf, stateOf } from "./view";

/** Estado de verificación del hallazgo (F3 — Verifier): verificado, falso
 * positivo audit-only (no publicado al VCS) o sin verificar. */
function Verified({ verified }: { verified: FindingView["verified"] }) {
	if (verified === true) {
		return <span class="text-severity-baja">✔ Verificado</span>;
	}
	if (verified === false) {
		return (
			<span class="text-severity-media">✖ Falso positivo (no publicado)</span>
		);
	}
	return <span class="text-text-muted">— Sin verificar</span>;
}

export function PrDetail({ prId }: { prId: number }) {
	const { pr, review, findings, diff, isLoading, error, diffError } =
		usePR(prId);
	// Contenedor del diff: las filas ancla [data-file][data-line] viven acá.
	const diffContainer = useRef<HTMLDivElement>(null);

	if (isLoading) {
		return <p class="text-text-muted">Cargando…</p>;
	}
	if (error !== null) {
		const notFound = error instanceof ApiError && error.status === 404;
		return (
			<Banner tone="error">
				{notFound
					? "Pull request no encontrado."
					: "No se pudo cargar el detalle del PR."}
			</Banner>
		);
	}
	if (pr === null) return null;

	/** Lleva el foco a la fila del diff que ancla el hallazgo. */
	const scrollToFinding = (file: string, line: number) => {
		diffContainer.current
			?.querySelector(`[data-file="${CSS.escape(file)}"][data-line="${line}"]`)
			?.scrollIntoView({ block: "center" });
	};

	const state = stateOf(pr.state);
	const reviewStatus = review !== null ? reviewStatusOf(review.status) : null;

	return (
		<section>
			<a href="#/prs" class="text-sm text-text-muted hover:text-text-primary">
				← Volver a la lista de PRs
			</a>

			<Card
				class="mt-3"
				title={`PR #${pr.number} · ${pr.repo.owner}/${pr.repo.name}`}
				actions={<Badge tone={state.tone}>{`● ${state.label}`}</Badge>}
			>
				<p class="font-mono text-sm text-text-muted">
					{`${pr.author} · ${pr.head_sha.slice(0, 7)} → ${pr.base_ref}`}
				</p>
			</Card>

			<Card title="Resumen" class="mt-3">
				{review === null || reviewStatus === null ? (
					<Banner tone="info">
						Este PR aún no tiene corridas de revisión.
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

			<Card title={`Hallazgos (${findings.length})`} class="mt-3">
				{findings.length === 0 ? (
					<p class="text-sm text-text-muted">Sin hallazgos registrados.</p>
				) : (
					<ul class="flex flex-col gap-3">
						{findings.map((finding) => {
							const severity = severityOf(finding.severity);
							return (
								<li
									key={finding.id}
									class="rounded-md border border-border-subtle p-3"
								>
									<div class="mb-1 flex flex-wrap items-center gap-2">
										<Badge tone={severity.tone}>{`● ${severity.label}`}</Badge>
										<Badge>{finding.category}</Badge>
										<Badge>{sourceOf(finding.source)}</Badge>
										<Verified verified={finding.verified} />
										<button
											type="button"
											class="ml-auto font-mono text-xs text-action-primary underline"
											onClick={() =>
												scrollToFinding(finding.file, finding.line)
											}
										>
											{`${finding.file}:${finding.line}`}
										</button>
									</div>
									<p class="text-sm whitespace-pre-wrap">{finding.body}</p>
									{finding.suggestion !== null && finding.suggestion !== "" && (
										<>
											<h3 class="mt-2 mb-1 text-xs font-semibold">
												Sugerencia
											</h3>
											<pre class="overflow-auto rounded-md border border-border-subtle bg-bg-elevated p-3 text-xs">
												<code>{finding.suggestion}</code>
											</pre>
										</>
									)}
								</li>
							);
						})}
					</ul>
				)}
			</Card>

			<Card title="Diff" class="mt-3">
				{diffError !== null ? (
					<Banner tone="warning">No se pudo obtener el diff del VCS.</Banner>
				) : diff === null ? (
					<p class="text-sm text-text-muted">Cargando diff…</p>
				) : (
					<div ref={diffContainer}>
						<DiffViewer
							diff={diff}
							highlights={findings.map((f) => ({ file: f.file, line: f.line }))}
						/>
					</div>
				)}
			</Card>
		</section>
	);
}
