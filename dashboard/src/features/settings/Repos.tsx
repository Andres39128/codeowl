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
import type { Provider, Repo } from "../../lib/types";
import { RepoModal } from "./RepoModal";

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
