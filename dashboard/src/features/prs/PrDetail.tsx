/** Detalle de PR (guía §6 F3): header, resumen de la corrida, findings con
 * estado de verificación y diff con anclas por hallazgo. Todo contenido del
 * VCS o del LLM se renderiza como TEXTO — el mermaid nunca se dibuja del lado
 * cliente y el escaping es el nativo de Preact, jamás innerHTML (§9.5). El
 * diff es degradable: si el VCS falla (502), el resto de la página sigue. */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "preact/hooks";
import { Badge } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { Card } from "../../components/Card";
import { DiffViewer } from "../../components/DiffViewer";
import { ApiError, api } from "../../lib/apiClient";
import type { FindingView, ReviewDetail } from "../../lib/types";
import { usePR } from "../../lib/usePR";
import { PromptModal } from "./PromptModal";
import {
	categoryHintOf,
	categoryOf,
	reviewStatusOf,
	severityOf,
	sourceOf,
	stateOf,
} from "./view";

/** Sugerencia del hallazgo: el cambio exacto que propone el modelo, listo
 * para aplicar. En GitHub viaja como bloque `suggestion` (se aplica con un
 * clic); acá se puede copiar y saltar al comentario real. */
function Sugerencia({
	suggestion,
	accepted,
	githubCommentID,
	prURL,
}: {
	suggestion: string;
	accepted: boolean | null;
	githubCommentID: string | null;
	prURL: string | null;
}) {
	const [copiado, setCopiado] = useState(false);

	async function copiar() {
		try {
			await navigator.clipboard.writeText(suggestion);
			setCopiado(true);
			setTimeout(() => setCopiado(false), 2000);
		} catch {
			// Sin permiso de portapapeles (contexto no seguro): el código
			// sigue visible para copiarlo a mano.
			setCopiado(false);
		}
	}

	return (
		<div class="mt-2">
			<div class="mb-1 flex flex-wrap items-center gap-2">
				<h3 class="text-xs font-semibold">Sugerencia</h3>
				<span
					class="text-xs text-text-muted"
					title="El cambio exacto que propone el modelo para arreglar lo que encontró: en GitHub se publica como bloque aplicable con un clic; acá lo podés copiar y pegar."
				>
					propuesta de código del modelo
				</span>
				{accepted === true && (
					<span
						class="text-xs text-severity-baja"
						title="El metrics job (§6 F5) detectó el código de la sugerencia en un commit posterior: quedó aplicada."
					>
						✔ Aplicada
					</span>
				)}
				{accepted === false && (
					<span
						class="text-xs text-text-muted"
						title="El metrics job (§6 F5) evaluó la sugerencia y no encontró su código en ningún commit posterior."
					>
						○ No aplicada
					</span>
				)}
				<button
					type="button"
					class="ml-auto rounded border border-border-subtle px-2 py-0.5 text-xs text-action-primary hover:bg-bg-elevated"
					onClick={copiar}
				>
					{copiado ? "✓ Copiado" : "Copiar"}
				</button>
				{githubCommentID !== null && prURL !== null && (
					<a
						href={`${prURL}#discussion_r${githubCommentID}`}
						target="_blank"
						rel="noreferrer"
						class="rounded border border-border-subtle px-2 py-0.5 text-xs text-action-primary hover:bg-bg-elevated"
						title="Abre el comentario en GitHub, donde el bloque suggestion se aplica con un clic."
					>
						Ver en GitHub
					</a>
				)}
			</div>
			<pre class="overflow-auto rounded-md border border-border-subtle bg-bg-elevated p-3 text-xs">
				<code>{suggestion}</code>
			</pre>
		</div>
	);
}

/** Estado de verificación del hallazgo (F3 — Verifier): verificado, falso
 * positivo audit-only (no publicado al VCS) o sin verificar. Un hallazgo
 * SAST no se verifica nunca: el linter es determinista (§3.3) — por eso no
 * puede llamarse "sin verificar", sino "determinista". */
function Verified({
	verified,
	source,
}: {
	verified: FindingView["verified"];
	source: FindingView["source"];
}) {
	if (source === "sast") {
		return (
			<span
				class="text-text-muted"
				title="Viene de un linter (ESLint, Ruff, golangci-lint, Gitleaks): el hallazgo es determinista, no lo contrasta el Verifier porque no hace falta — el linter ya aplicó la regla."
			>
				Determinista (SAST)
			</span>
		);
	}
	if (verified === true) {
		return (
			<span
				class="text-severity-baja"
				title="El Verifier contrastó el reclamo contra el código del diff y lo corroboró. Es el estado por defecto: el hallazgo se publica."
			>
				✔ Verificado
			</span>
		);
	}
	if (verified === false) {
		return (
			<span
				class="text-severity-media"
				title="El Verifier confirmó que es un falso positivo: NO se publicó en el PR. Queda acá solo para auditoría (§6 F3)."
			>
				✖ Falso positivo (no publicado)
			</span>
		);
	}
	return (
		<span
			class="text-text-muted"
			title="No se pudo contrastar: el Verifier no corrió (falta proveedor del rol cheap) o el lote se descartó tras los reintentos (§9.8). El hallazgo se publica igual, sin confirmar."
		>
			— Sin verificar
		</span>
	);
}

/** Auditoría de una corrida (§9.9): qué proveedor atendió cada rol, tokens
 * quemados, duración y hallazgos. Se abre desde el resumen del PR. */
function CorridaDetail({ reviewId }: { reviewId: number }) {
	const detalle = useQuery({
		queryKey: ["review", reviewId],
		queryFn: () => api<ReviewDetail>(`/api/reviews/${reviewId}`),
	});
	if (detalle.isPending)
		return <p class="text-sm text-text-muted">Cargando…</p>;
	if (detalle.isError) {
		return <Banner tone="error">No se pudo cargar la corrida.</Banner>;
	}
	const d = detalle.data;
	const totalTokens = d.llm_usage.reduce<number>(
		(acc, u) => acc + u.tokens_in + u.tokens_out,
		0,
	);
	return (
		<div class="mt-3 rounded-md border border-border-subtle bg-bg-surface p-3 text-sm">
			<div class="mb-2 flex flex-wrap items-center gap-3">
				<span class="font-semibold">Corrida #{d.id}</span>
				<span class="text-text-muted">
					Duración: {(d.duration_ms / 1000).toFixed(0)}s (
					{Math.floor(d.duration_ms / 60000)}m{" "}
					{Math.round((d.duration_ms % 60000) / 1000)}s)
				</span>
				<span class="text-text-muted">
					{totalTokens.toLocaleString("es-AR")} tokens
				</span>
			</div>
			<table class="w-full text-xs">
				<caption class="sr-only">Uso de LLM por rol en la corrida</caption>
				<thead>
					<tr class="text-left text-text-muted">
						<th class="py-1 font-medium">Rol</th>
						<th class="py-1 font-medium">Modelo</th>
						<th class="py-1 font-medium">Tokens in</th>
						<th class="py-1 font-medium">Tokens out</th>
					</tr>
				</thead>
				<tbody>
					{d.llm_usage.map((u) => (
						<tr
							key={`${u.role}-${u.model}`}
							class="border-t border-border-subtle"
						>
							<td class="py-1">{u.role}</td>
							<td class="py-1">{u.model}</td>
							<td class="py-1">{u.tokens_in.toLocaleString("es-AR")}</td>
							<td class="py-1">{u.tokens_out.toLocaleString("es-AR")}</td>
						</tr>
					))}
				</tbody>
			</table>
			<p class="mt-2 text-xs text-text-muted">
				Hallazgos: {d.findings_by_severity.high} alta ·{" "}
				{d.findings_by_severity.medium} media · {d.findings_by_severity.low}{" "}
				baja
			</p>
		</div>
	);
}

/** Agrupa hallazgos por archivo preservando el orden de aparición. */
function porArchivo(list: FindingView[]): [string, FindingView[]][] {
	const grupos = new Map<string, FindingView[]>();
	for (const f of list) {
		const g = grupos.get(f.file) ?? [];
		g.push(f);
		grupos.set(f.file, g);
	}
	return [...grupos.entries()];
}

/** Un hallazgo del triage (S4): badges, cuerpo, sugerencia accionable y el
 * botón "No aplica" que registra la huella para las corridas futuras. */
function HallazgoItem({
	finding,
	onScroll,
	prURL,
	diff,
	repo,
}: {
	finding: FindingView;
	onScroll: (file: string, line: number) => void;
	prURL: string | null;
	diff: string;
	repo: { owner: string; name: string; number: number };
}) {
	const [promptAbierto, setPromptAbierto] = useState(false);
	const queryClient = useQueryClient();
	const dismiss = useMutation({
		mutationFn: () =>
			api(`/api/findings/${finding.id}/dismiss`, {
				method: "POST",
				body: { reason: "" },
			}),
		onSuccess: () => queryClient.invalidateQueries({ queryKey: ["pr"] }),
	});
	const sev = severityOf(finding.severity);
	return (
		<li
			key={finding.id}
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
				<Verified verified={finding.verified} source={finding.source} />
				{finding.dismissed && (
					<span
						class="text-xs text-text-muted"
						title="Alguien marcó esta huella como no aplica: queda para auditoría y no vuelve a molestar."
					>
						✕ No aplica
					</span>
				)}
				{finding.persistent && (
					<span
						class="text-xs text-text-muted"
						title="La misma huella (archivo + categoría) apareció en la corrida anterior de este PR: sigue sin arreglarse."
					>
						↻ Persistente
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
				<button
					type="button"
					class={`rounded border border-border-subtle px-2 py-0.5 text-xs text-action-primary hover:bg-bg-elevated ${finding.dismissed ? "ml-auto" : ""}`}
					title="Arma un prompt con todo el contexto de este hallazgo para pegar en tu asistente de IA: te ayuda a corregirlo o a entenderlo."
					onClick={() => setPromptAbierto(true)}
				>
					Arreglar con IA
				</button>
				{promptAbierto && (
					<PromptModal
						finding={finding}
						repo={repo}
						diff={diff}
						onClose={() => setPromptAbierto(false)}
					/>
				)}
			</div>
			<p class="text-sm break-words whitespace-pre-wrap">{finding.body}</p>
			{finding.suggestion !== null && finding.suggestion !== "" && (
				<Sugerencia
					suggestion={finding.suggestion}
					accepted={finding.accepted}
					githubCommentID={finding.github_comment_id}
					prURL={prURL}
				/>
			)}
		</li>
	);
}

export function PrDetail({ prId }: { prId: number }) {
	const {
		pr,
		review,
		findings,
		resolved,
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
	// El detalle trae los hallazgos de TODAS las corridas del PR (§3.3: la
	// atribución por corrida). Por defecto se ve solo la última — la que
	// résumé el comentario publicado — con un toggle para auditar el
	// histórico completo.
	const [onlyLatest, setOnlyLatest] = useState(true);
	// Corrida cuya auditoría está abierta (roles, tokens, duración) — null
	// cuando el panel está cerrado.
	const [verCorrida, setVerCorrida] = useState<number | null>(null);
	// Triage S4: los marcados "no aplica" se ocultan por defecto y la
	// agrupación por archivo colapsa listas largas.
	const [mostrarMarcados, setMostrarMarcados] = useState(false);
	const [agruparPorArchivo, setAgruparPorArchivo] = useState(false);
	const queryClient = useQueryClient();
	// Reintentar corrida (S5): el remedio del detalle cuando la última
	// corrida terminó failed o partial.
	const reintentar = useMutation({
		mutationFn: () =>
			api<{ enqueued: boolean }>(`/api/prs/${prId}/review`, { method: "POST" }),
		onSuccess: () => queryClient.invalidateQueries({ queryKey: ["pr", prId] }),
	});
	const latestReviewID = review?.id ?? null;

	const fileOptions = useMemo(
		() => [...new Set(findings.map((f) => f.file))].sort(),
		[findings],
	);
	const scoped = useMemo(
		() =>
			onlyLatest && latestReviewID !== null
				? findings.filter((f) => f.review_id === latestReviewID)
				: findings,
		[findings, onlyLatest, latestReviewID],
	);
	const visible = useMemo(() => {
		const q = query.trim().toLowerCase();
		return scoped.filter((f) => {
			// S4: los marcados "no aplica" quedan ocultos salvo elección
			// explícita — molestar de nuevo es lo que querés evitar.
			if (f.dismissed && !mostrarMarcados) return false;
			if (severity !== "" && f.severity !== severity) return false;
			if (source !== "" && f.source !== source) return false;
			if (file !== "" && f.file !== file) return false;
			if (q !== "" && !f.body.toLowerCase().includes(q)) return false;
			return true;
		});
	}, [scoped, severity, source, file, query, mostrarMarcados]);

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

	// Contexto que viaja al creador de prompts (S1): el diff crudo para el
	// snippet del archivo y la identidad del repo + PR.
	const diffTexto = diff ?? "";
	const repoInfo = {
		owner: pr.repo.owner,
		name: pr.repo.name,
		number: pr.number,
	};
	// URL del PR en el VCS: el permalink del comentario real (donde el
	// bloque suggestion de GitHub se aplica con un clic). GitLab usa #note_.
	const prURL =
		pr.repo.vcs === "gitlab"
			? `https://gitlab.com/${pr.repo.owner}/${pr.repo.name}/-/merge_requests/${pr.number}`
			: `https://github.com/${pr.repo.owner}/${pr.repo.name}/pull/${pr.number}`;

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
							{(review.status === "failed" || review.status === "partial") && (
								<button
									type="button"
									disabled={reintentar.isPending}
									class="ml-auto rounded border border-border-subtle px-2 py-1 text-xs text-action-primary hover:bg-bg-elevated disabled:opacity-50"
									title="Encola una nueva revisión de este PR con su identidad actual, sin esperar webhook."
									onClick={() => reintentar.mutate()}
								>
									{reintentar.isPending ? "Encolando…" : "Reintentar corrida"}
								</button>
							)}
						</div>
						{reintentar.isSuccess && (
							<Banner tone="success" class="mb-2">
								Revisión encolada: la nueva corrida aparece acá cuando el worker
								la toma.
							</Banner>
						)}
						{reintentar.isError && (
							<Banner tone="error" class="mb-2">
								{reintentar.error instanceof ApiError
									? reintentar.error.message
									: "No se pudo encolar la revisión."}
							</Banner>
						)}
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
						<button
							type="button"
							class="mt-3 rounded border border-border-subtle px-2 py-1 text-xs text-action-primary hover:bg-bg-elevated"
							onClick={() =>
								setVerCorrida(verCorrida === null ? review.id : null)
							}
						>
							{verCorrida === null
								? "Ver corrida (roles, tokens, duración)"
								: "Cerrar corrida"}
						</button>
						{verCorrida !== null && <CorridaDetail reviewId={verCorrida} />}
					</>
				)}
			</Card>

			{/* El diff va ANTES de los hallazgos: el revisor lee el código
			 * primero y los hallazgos son el comentario sobre ese código
			 * (§5.3). Con cientos de hallazgos, el diff al final quedaba
			 * inalcanzable. */}
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
			<Card
				title={
					visible.length === scoped.length
						? `Hallazgos (${scoped.length})`
						: `Hallazgos (${visible.length} de ${scoped.length})`
				}
				class="mt-3"
			>
				{" "}
				{findings.length === 0 ? (
					<p class="text-sm text-text-muted">Sin hallazgos registrados.</p>
				) : (
					<>
						{/* Leyenda: qué significa cada badge. Sin esto, "Sin
						 * verificar" en un hallazgo de linter se lee como
						 * "dudoso" cuando en realidad es determinista. */}
						<p class="mb-3 text-xs text-text-muted">
							<strong class="text-text-primary">Cómo leer esto:</strong>{" "}
							<strong>Severidad</strong> (Alta/Media/Baja) y{" "}
							<strong>Categoría</strong> (dónde cae el problema).{" "}
							<strong>Origen</strong>: SAST = lo detectó un linter determinista
							(ESLint, Ruff, golangci-lint, Clippy, Gitleaks); LLM = lo
							detectaron los agentes que leen el diff.{" "}
							<strong>Verificación</strong>: el Verifier contrasta los hallazgos
							del LLM contra el código — "Verificado" = corroborado; "Falso
							positivo" = no se publicó en el PR (solo auditoría); "Sin
							verificar" = no se pudo contrastar y se publica igual.{" "}
							<strong>Sugerencia</strong>: el cambio exacto que propone el
							modelo para arreglar lo que encontró — en GitHub se publica como
							bloque aplicable con un clic; acá lo copiás o saltás al
							comentario.{" "}
							<em>Pasa el mouse sobre cada badge para el detalle.</em>
						</p>
						{/* Filtros del revisor: acotan el conjunto sin perder el
						 * total — el título dice cuántos quedan de cuántos hay. */}
						<div class="mb-3 flex flex-wrap items-end gap-3">
							{/* Corrida: por defecto la última (la que se publicó);
							 * el toggle abre el histórico de todas. */}
							<div class="flex flex-col gap-1">
								<label class="text-xs text-text-muted" for="f-latest">
									Corrida
								</label>
								<div class="flex items-center gap-2">
									<input
										id="f-latest"
										type="checkbox"
										checked={onlyLatest}
										onInput={(event) =>
											setOnlyLatest((event.target as HTMLInputElement).checked)
										}
									/>
									<span class="text-xs text-text-muted">
										{onlyLatest ? "Solo la última" : "Todas las corridas"}
									</span>
								</div>
							</div>
							{/* Triage S4: los "no aplica" ocultos por defecto y
							 * la agrupación por archivo para listas largas. */}
							<div class="flex flex-col gap-1">
								<label class="text-xs text-text-muted" for="f-dismissed">
									Marcados
								</label>
								<div class="flex items-center gap-2">
									<input
										id="f-dismissed"
										type="checkbox"
										checked={mostrarMarcados}
										onInput={(event) =>
											setMostrarMarcados(
												(event.target as HTMLInputElement).checked,
											)
										}
									/>
									<span class="text-xs text-text-muted">
										{mostrarMarcados ? "Mostrar marcados" : "Ocultar marcados"}
									</span>
								</div>
							</div>
							<div class="flex flex-col gap-1">
								<label class="text-xs text-text-muted" for="f-group">
									Vista
								</label>
								<div class="flex items-center gap-2">
									<input
										id="f-group"
										type="checkbox"
										checked={agruparPorArchivo}
										onInput={(event) =>
											setAgruparPorArchivo(
												(event.target as HTMLInputElement).checked,
											)
										}
									/>
									<span class="text-xs text-text-muted">
										{agruparPorArchivo ? "Agrupar por archivo" : "Lista plana"}
									</span>
								</div>
							</div>
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
						) : agruparPorArchivo ? (
							<div class="flex flex-col gap-2">
								{porArchivo(visible).map(([archivo, grupo]) => (
									<details key={archivo} open={true} role="group">
										<summary class="cursor-pointer rounded-md border border-border-subtle px-3 py-1.5 text-sm">
											<span class="font-mono text-xs">{archivo}</span>
											<span class="ml-2 text-xs text-text-muted">{`${grupo.length} hallazgo${grupo.length === 1 ? "" : "s"}`}</span>
										</summary>
										<ul class="mt-2 flex flex-col gap-3 border-l border-border-subtle pl-3">
											{grupo.map((finding) => (
												<HallazgoItem
													key={finding.id}
													finding={finding}
													onScroll={scrollToFinding}
													prURL={prURL}
													diff={diffTexto}
													repo={repoInfo}
												/>
											))}
										</ul>
									</details>
								))}
							</div>
						) : (
							<ul class="flex flex-col gap-3">
								{visible.map((finding) => (
									<HallazgoItem
										key={finding.id}
										finding={finding}
										onScroll={scrollToFinding}
										prURL={prURL}
										diff={diffTexto}
										repo={repoInfo}
									/>
								))}
							</ul>
						)}
					</>
				)}
			</Card>

			{/* Resueltos entre corridas (S2): huellas de la corrida anterior que
			 * ya no aparecen — colapsada para no competir con lo que SÍ hay que
			 * arreglar. Solo existe si hay resueltos (sin placeholders vacíos). */}
			{resolved.length > 0 && (
				<details class="mt-3 rounded-md border border-border-subtle px-3 py-2">
					<summary class="cursor-pointer text-sm">
						<strong>{`Resueltos en esta corrida (${resolved.length})`}</strong>
						<span class="ml-2 text-xs text-text-muted">
							hallazgos de la corrida anterior que ya no aparecen
						</span>
					</summary>
					<ul class="mt-2 flex flex-col gap-2 border-l border-border-subtle pl-3">
						{resolved.map((f) => {
							const sev = severityOf(f.severity);
							return (
								<li key={f.id} class="py-1">
									<div class="mb-1 flex flex-wrap items-center gap-2">
										<Badge tone={sev.tone}>{`● ${sev.label}`}</Badge>
										<span class="font-mono text-xs text-action-primary">{`${f.file}:${f.line}`}</span>
										<span class="text-xs text-text-muted">{categoryOf(f.category)}</span>
									</div>
									<p class="text-sm text-text-muted break-words">{f.body}</p>
								</li>
							);
						})}
					</ul>
				</details>
			)}

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
		</section>
	);
}
