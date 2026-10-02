/** Settings — proveedores LLM (BYOK cifrado, §9.2): alta, edición, toggle de
 * habilitación, prueba de conexión y baja. Exclusivo del admin (§3.4). */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "preact/hooks";
import { Badge } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { Button } from "../../components/Button";
import { type Column, Table } from "../../components/Table";
import { api } from "../../lib/apiClient";
import type { Provider } from "../../lib/types";
import { ProviderModal } from "./ProviderModal";

/** Resultado de la prueba de conexión (api/settings.go: ok=false NO es error
 * HTTP — el proveedor que no responde es un resultado, no un fallo). */
interface TestResult {
	ok: boolean;
	error?: string;
	latency_ms: number;
}

export function Providers() {
	const queryClient = useQueryClient();
	const providers = useQuery({
		queryKey: ["providers"],
		queryFn: () => api<Provider[]>("/api/providers"),
	});
	// undefined = modal cerrado; null = alta; Provider = edición.
	const [editing, setEditing] = useState<Provider | null | undefined>(
		undefined,
	);
	const [tests, setTests] = useState<Record<number, TestResult | "loading">>(
		{},
	);

	const invalidate = () =>
		queryClient.invalidateQueries({ queryKey: ["providers"] });

	const toggle = useMutation({
		mutationFn: (provider: Provider) =>
			api<Provider>(`/api/providers/${provider.id}`, {
				method: "PUT",
				// El update es reemplazo completo: key vacía = conservar la actual.
				body: {
					base_url: provider.base_url,
					model: provider.model,
					api_key: "",
					role: provider.role,
					priority: provider.priority,
					enabled: !provider.enabled,
				},
			}),
		onSuccess: invalidate,
	});

	const remove = useMutation({
		mutationFn: (provider: Provider) =>
			api(`/api/providers/${provider.id}`, { method: "DELETE" }),
		onSuccess: invalidate,
	});

	const test = useMutation({
		mutationFn: (id: number) =>
			api<TestResult>(`/api/providers/${id}/test`, { method: "POST" }),
		onMutate: (id) => setTests((prev) => ({ ...prev, [id]: "loading" })),
		onSuccess: (result, id) => setTests((prev) => ({ ...prev, [id]: result })),
		onError: (error, id) =>
			setTests((prev) => ({
				...prev,
				[id]: {
					ok: false,
					error: error instanceof Error ? error.message : "error de conexión",
					latency_ms: 0,
				},
			})),
	});

	const mutationError = toggle.error ?? remove.error;

	const columns: Column<Provider>[] = [
		{ key: "model", header: "Modelo" },
		{
			key: "base_url",
			header: "Base URL",
			render: (provider) => (
				<span class="block max-w-40 truncate" title={provider.base_url}>
					{provider.base_url}
				</span>
			),
		},
		{
			key: "role",
			header: "Rol",
			render: (provider) => <Badge>{provider.role}</Badge>,
		},
		{ key: "priority", header: "Prioridad" },
		{
			key: "enabled",
			header: "Habilitado",
			render: (provider) => (
				<input
					type="checkbox"
					checked={provider.enabled}
					disabled={toggle.isPending}
					onChange={() => toggle.mutate(provider)}
					aria-label={`Habilitar proveedor ${provider.model}`}
				/>
			),
		},
		{
			key: "actions",
			header: "Acciones",
			render: (provider) => (
				<span class="flex gap-1">
					<Button
						variant="ghost"
						onClick={() => test.mutate(provider.id)}
						disabled={tests[provider.id] === "loading"}
					>
						Probar
					</Button>
					<Button variant="ghost" onClick={() => setEditing(provider)}>
						Editar
					</Button>
					<Button
						variant="ghost"
						onClick={() => {
							if (
								globalThis.confirm(`¿Eliminar el proveedor ${provider.model}?`)
							) {
								remove.mutate(provider);
							}
						}}
					>
						Eliminar
					</Button>
				</span>
			),
		},
	];

	return (
		<section>
			<div class="mb-4 flex items-center justify-between gap-4">
				<h1 class="text-xl font-semibold">Proveedores LLM</h1>
				<Button onClick={() => setEditing(null)}>Agregar proveedor</Button>
			</div>

			{mutationError && (
				<Banner tone="error" class="mb-4">
					{mutationError instanceof Error
						? mutationError.message
						: "La operación falló."}
				</Banner>
			)}

			{providers.isError ? (
				<Banner tone="error">No se pudieron cargar los proveedores.</Banner>
			) : providers.isPending ? (
				<p class="text-text-muted">Cargando…</p>
			) : (
				<>
					<Table
						caption="Proveedores LLM configurados"
						columns={columns}
						rows={providers.data}
						getRowKey={(provider) => String(provider.id)}
					/>
					{providers.data.map((provider) => {
						const result = tests[provider.id];
						if (result === undefined) return null;
						return (
							<p
								key={`test-${provider.id}`}
								class={`mt-1 text-xs ${
									result === "loading"
										? "text-text-muted"
										: result.ok
											? "text-severity-baja"
											: "text-severity-alta"
								}`}
							>
								{provider.model}:{" "}
								{result === "loading" ? (
									<span>Probando…</span>
								) : result.ok ? (
									<span>{`OK · ${result.latency_ms} ms`}</span>
								) : (
									<span>{`Falló · ${result.error ?? "sin respuesta"}`}</span>
								)}
							</p>
						);
					})}
				</>
			)}

			{editing !== undefined && (
				<ProviderModal
					provider={editing}
					onClose={() => setEditing(undefined)}
				/>
			)}
		</section>
	);
}
