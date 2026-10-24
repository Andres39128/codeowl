/** Settings — repos conectados: conectar es registrar; la desconexión es el
 * flag enabled, sin delete (§3.5). Exclusivo del admin (§3.4). Muestra una
 * guarda si no hay proveedor LLM con rol review habilitado (§9.6). */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "preact/hooks";
import { Badge } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { Button } from "../../components/Button";
import { type Column, Table } from "../../components/Table";
import { ApiError, api } from "../../lib/apiClient";
import type { Provider, Repo, RepoHealth } from "../../lib/types";
import { RepoModal } from "./RepoModal";

/** Los checks de salud de un repo, en una línea: qué está bien y qué lo
 * bloquea, con el remedio. Es la respuesta a "¿por qué no se revisó mi
 * PR?" (§9.9). */
function SaludRepo({ repo }: { repo: Repo }) {
	const salud = useQuery({
		queryKey: ["repo-health", repo.id],
		queryFn: () => api<RepoHealth>(`/api/repos/${repo.id}/health`),
	});
	const queryClient = useQueryClient();
	const revisar = useMutation({
		mutationFn: () =>
			api<{ enqueued: boolean; pr_number: number }>(
				`/api/repos/${repo.id}/review`,
				{ method: "POST" },
			),
		onSuccess: () =>
			queryClient.invalidateQueries({
				queryKey: ["repo-health", repo.id],
			}),
	});
	if (!repo.enabled) {
		return <span class="text-xs text-text-muted">repo desconectado</span>;
	}
	if (salud.isPending) {
		return <span class="text-xs text-text-muted">verificando…</span>;
	}
	if (salud.isError) {
		return <span class="text-xs text-text-muted">salud no disponible</span>;
	}
	const h = salud.data;
	const checks: { ok: boolean; texto: string; remedio?: string }[] = [];
	if (h.app_installed !== null) {
		checks.push(
			h.app_installed
				? { ok: true, texto: "App instalada" }
				: {
						ok: false,
						texto: "App sin instalar",
						remedio: "instalala en el repo (github.com/apps/codeowlapp)",
					},
		);
	}
	checks.push(
		h.last_delivery_at !== null
			? {
					ok: true,
					texto: `Webhook activo (último: ${new Date(h.last_delivery_at).toLocaleString("es-AR")})`,
				}
			: {
					ok: false,
					texto: "Sin webhook recibido",
					remedio:
						"la instancia necesita URL pública para recibir eventos (túnel o servidor)",
				},
	);
	checks.push(
		h.has_review_provider
			? { ok: true, texto: "Proveedor review activo" }
			: {
					ok: false,
					texto: "Sin proveedor review",
					remedio: "crear uno en Settings → Proveedores",
				},
	);
	const todoOk = checks.every((c) => c.ok);
	return (
		<div class="flex flex-col gap-1">
			<ul class="flex flex-col gap-1">
				{checks.map((c) => (
					<li
						key={c.texto}
						class={`text-xs ${c.ok ? "text-severity-baja" : "text-severity-alta"}`}
					>
						{c.ok ? "✓" : "✗"} {c.texto}
						{!c.ok && c.remedio !== undefined && (
							<span class="text-text-muted">{` — ${c.remedio}`}</span>
						)}
					</li>
				))}
				{h.latest_open_pr === null && (
					<li class="text-xs text-text-muted">— sin PRs abiertos</li>
				)}
			</ul>
			<div class="flex items-center gap-2">
				<button
					type="button"
					disabled={!todoOk || h.latest_open_pr === null || revisar.isPending}
					class="rounded border border-border-subtle px-2 py-1 text-xs text-action-primary disabled:opacity-50 hover:bg-bg-elevated"
					title={
						h.latest_open_pr === null
							? "sin PRs abiertos: no hay nada que revisar"
							: "encola la revisión del PR abierto más reciente sin esperar webhook"
					}
					onClick={() => revisar.mutate()}
				>
					{revisar.isPending
						? "Encolando…"
						: `Revisar ahora${h.latest_open_pr !== null ? ` (PR #${h.latest_open_pr.number})` : ""}`}
				</button>
				{revisar.isError && (
					<span class="text-xs text-severity-alta">
						{revisar.error instanceof ApiError
							? revisar.error.message
							: "no se pudo encolar"}
					</span>
				)}
				{revisar.isSuccess && (
					<span class="text-xs text-severity-baja">
						✓ encolado (PR #{revisar.data.pr_number})
					</span>
				)}
			</div>
		</div>
	);
}

export function Repos() {
	const queryClient = useQueryClient();
	const repos = useQuery({
		queryKey: ["repos"],
		queryFn: () => api<Repo[]>("/api/repos"),
	});
	// La guarda §9.6 se replica en la UI: conectar sin proveedor review
	// habilitado termina en 422 — mejor avisar antes.
	const providers = useQuery({
		queryKey: ["providers"],
		queryFn: () => api<Provider[]>("/api/providers"),
	});
	const [connecting, setConnecting] = useState(false);

	const hasReviewProvider = (providers.data ?? []).some(
		(provider) => provider.enabled && provider.role === "review",
	);

	const toggle = useMutation({
		mutationFn: (repo: Repo) =>
			api<Repo>(`/api/repos/${repo.id}`, {
				method: "PUT",
				// Desconectar/reconectar = flag enabled; lo demás no se toca.
				body: { enabled: !repo.enabled },
			}),
		onSuccess: () => queryClient.invalidateQueries({ queryKey: ["repos"] }),
	});

	const toggleMessage =
		toggle.error instanceof ApiError
			? toggle.error.message
			: toggle.error
				? "No se pudo cambiar el estado del repo."
				: null;

	const columns: Column<Repo>[] = [
		{
			key: "vcs",
			header: "VCS",
			render: (repo) => (
				<Badge>{repo.vcs === "github" ? "GitHub" : "GitLab"}</Badge>
			),
		},
		{
			key: "name",
			header: "Repo",
			render: (repo) => `${repo.owner}/${repo.name}`,
		},
		{
			key: "enabled",
			header: "Conectado",
			render: (repo) => (
				<input
					type="checkbox"
					checked={repo.enabled}
					disabled={toggle.isPending}
					onChange={() => toggle.mutate(repo)}
					aria-label={`Conectar ${repo.owner}/${repo.name}`}
				/>
			),
		},
		{
			key: "language",
			header: "Lenguaje",
			render: (repo) => repo.language || "—",
		},
		{
			key: "salud",
			header: "Salud",
			render: (repo) => <SaludRepo repo={repo} />,
		},
	];

	return (
		<section>
			<div class="mb-4 flex items-center justify-between gap-4">
				<h1 class="text-xl font-semibold">Repos conectados</h1>
				<Button onClick={() => setConnecting(true)}>Conectar repo</Button>
			</div>

			{providers.data && !hasReviewProvider && (
				<Banner tone="warning" class="mb-4">
					Necesitas al menos un proveedor LLM con rol review habilitado antes de
					conectar un repo
				</Banner>
			)}

			{toggleMessage && (
				<Banner tone="error" class="mb-4">
					{toggleMessage}
				</Banner>
			)}

			{repos.isError ? (
				<Banner tone="error">No se pudieron cargar los repos.</Banner>
			) : repos.isPending ? (
				<p class="text-text-muted">Cargando…</p>
			) : (
				<Table
					caption="Repos conectados"
					columns={columns}
					rows={repos.data}
					getRowKey={(repo) => String(repo.id)}
				/>
			)}

			{connecting && <RepoModal onClose={() => setConnecting(false)} />}
		</section>
	);
}
