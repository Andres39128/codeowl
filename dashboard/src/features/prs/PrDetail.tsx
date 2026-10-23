/** Detalle de PR (guía §6 F3): header, resumen de la corrida, findings con
 * estado de verificación y diff con anclas por hallazgo. Todo contenido del
 * VCS o del LLM se renderiza como TEXTO — el mermaid nunca se dibuja del lado
 * cliente y el escaping es el nativo de Preact, jamás innerHTML (§9.5). El
 * diff es degradable: si el VCS falla (502), el resto de la página sigue. */

import { useMemo, useRef, useState } from "preact/hooks";
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
	const {
		pr,
		review,
		findings,
		changeStack,
		diff,
		isLoading,
		error,
		diffError,
	} = usePR(prId);
	// Contenedor del diff: las filas ancla [data-file][data-line] viven acá.
	const diffContainer = useRef<HTMLDivElement>(null);

	// Filtros del revisor: una corrida puede publicar cientos de hallazgos
	// (SAST + LLM) y revisarlos de a uno no escala — el revisor acota por
	// severidad, origen, archivo y texto libre. Todo client-side: el listado
	// ya vino completo del API (mismo criterio que los filtros de triage).
	const [severity, setSeverity] = useState("");
	const [source, setSource] = useState("");
	const [file, setFile] = useState("");
	const [query, setQuery] = useState("");

	const fileOptions = useMemo(
		() => [...new Set(findings.map((f) => f.file))].sort(),
		[findings],
	);
	const visible = useMemo(() => {
		const q = query.trim().toLowerCase();
		return findings.filter((f) => {
			if (severity !== "" && f.severity !== severity) return false;
			if (source !== "" && f.source !== source) return false;
			if (file !== "" && f.file !== file) return false;
			if (q !== "" && !f.body.toLowerCase().includes(q)) return false;
			return true;
		});
	}, [findings, severity, source, file, query]);

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
		<section class="min-w-0">
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

			<Card
				title={
					visible.length === findings.length
						? `Hallazgos (${findings.length})`
						: `Hallazgos (${visible.length} de ${findings.length})`
				}
				class="mt-3"
			>
				{findings.length === 0 ? (
					<p class="text-sm text-text-muted">Sin hallazgos registrados.</p>
				) : (
					<>
						{/* Filtros del revisor: acotan el conjunto sin perder el
						 * total — el título dice cuántos quedan de cuántos hay. */}
						<div class="mb-3 flex flex-wrap items-end gap-3">
							<div class="flex flex-col gap-1">
								<label class="text-xs text-text-muted" for="f-severity">
									Severidad
								</label>
								<select
									id="f-severity"
									class="rounded-md border border-border-subtle bg-bg-surface px-2 py-1 text-sm"
									value={severity}
									// onInput y no onChange: convención de Preact (§5).
									onInput={(event) =>
										setSeverity((event.target as HTMLSelectElement).value)
									}
								>
									<option value="">Todas</option>
									<option value="high">Alta</option>
									<option value="medium">Media</option>
									<option value="low">Baja</option>
								</select>
							</div>
							<div class="flex flex-col gap-1">
								<label class="text-xs text-text-muted" for="f-source">
									Origen
								</label>
								<select
									id="f-source"
									class="rounded-md border border-border-subtle bg-bg-surface px-2 py-1 text-sm"
									value={source}
									onInput={(event) =>
										setSource((event.target as HTMLSelectElement).value)
									}
								>
									<option value="">Todos</option>
									<option value="llm">LLM</option>
									<option value="sast">SAST</option>
								</select>
							</div>
							<div class="flex flex-col gap-1">
								<label class="text-xs text-text-muted" for="f-file">
									Archivo
								</label>
								<select
									id="f-file"
									class="max-w-64 rounded-md border border-border-subtle bg-bg-surface px-2 py-1 text-sm"
									value={file}
									onInput={(event) =>
										setFile((event.target as HTMLSelectElement).value)
									}
								>
									<option value="">Todos</option>
									{fileOptions.map((f) => (
										<option key={f} value={f}>
											{f}
										</option>
									))}
								</select>
							</div>
							<div class="flex flex-col gap-1">
								<label class="text-xs text-text-muted" for="f-query">
									Buscar
								</label>
								<input
									id="f-query"
									type="search"
									class="rounded-md border border-border-subtle bg-bg-surface px-2 py-1 text-sm"
									placeholder="texto del hallazgo"
									value={query}
									onInput={(event) =>
										setQuery((event.target as HTMLInputElement).value)
									}
								/>
							</div>
						</div>
						{visible.length === 0 ? (
							<p class="text-sm text-text-muted">
								Ningún hallazgo cumple los filtros elegidos.
							</p>
						) : (
							<ul class="flex flex-col gap-3">
								{visible.map((finding) => {
									const sev = severityOf(finding.severity);
									return (
										<li
											key={finding.id}
											class="rounded-md border border-border-subtle p-3"
										>
											<div class="mb-1 flex flex-wrap items-center gap-2">
												<Badge tone={sev.tone}>{`● ${sev.label}`}</Badge>
												<Badge>{finding.category}</Badge>
												<Badge>{sourceOf(finding.source)}</Badge>
												<Verified verified={finding.verified} />
												<button
													type="button"
													class="ml-auto max-w-full break-all text-left font-mono text-xs text-action-primary underline"
													onClick={() =>
														scrollToFinding(finding.file, finding.line)
													}
												>
													{`${finding.file}:${finding.line}`}
												</button>
											</div>
											<p class="text-sm break-words whitespace-pre-wrap">
												{finding.body}
											</p>
											{finding.suggestion !== null &&
												finding.suggestion !== "" && (
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
					</>
				)}
			</Card>

			{/* Change stack (F6 §6): solo existe cuando la última corrida pudo
			 * calcular cohortes — sin índice del repo el campo no viene y no se
			 * renderiza nada (degradación visible, sin placeholders vacíos). */}
			{changeStack !== null && changeStack.length > 0 && (
				<Card title="Change Stack" class="mt-3">
					<p class="mb-3 text-sm text-text-muted">
						Cohortes de dependencia del diff: el nivel 0 es la base (revisalo
						primero); las pruebas van siempre al final.
					</p>
					<ul class="flex flex-col gap-3">
						{changeStack.map((cohort) => (
							<li
								key={`${cohort.kind}-${cohort.level}`}
								class="rounded-md border border-border-subtle p-3"
							>
								<div class="mb-1 flex flex-wrap items-center gap-2">
									<Badge>{`Nivel ${cohort.level}`}</Badge>
									{cohort.kind === "tests" && <Badge>Pruebas</Badge>}
									<span class="text-xs text-text-muted">{`${cohort.files.length} archivo${cohort.files.length === 1 ? "" : "s"}`}</span>
								</div>
								<ul class="flex flex-col gap-0.5 font-mono text-xs text-text-muted">
									{cohort.files.map((file) => (
										<li key={file}>{file}</li>
									))}
								</ul>
							</li>
						))}
					</ul>
				</Card>
			)}

			<Card title="Diff" class="mt-3">
				{diffError !== null ? (
					<Banner tone="warning">No se pudo obtener el diff del VCS.</Banner>
				) : diff === null ? (
					<p class="text-sm text-text-muted">Cargando diff…</p>
				) : (
					<div ref={diffContainer} class="min-w-0 max-w-full">
						<DiffViewer
							diff={diff}
							highlights={visible.map((f) => ({ file: f.file, line: f.line }))}
						/>
					</div>
				)}
			</Card>
		</section>
	);
}
