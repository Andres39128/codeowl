/** Modal de alta/edición de proveedor LLM. En edición la api_key vacía
 * conserva la guardada (cifrada en §9.2); el PUT es reemplazo completo, así
 * que reenvía base_url/model/role/priority/enabled. */

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "preact/hooks";
import { Banner } from "../../components/Banner";
import { Button } from "../../components/Button";
import { Field, inputClass } from "../../components/Field";
import { Modal } from "../../components/Modal";
import { ApiError, api } from "../../lib/apiClient";
import type { Provider } from "../../lib/types";

interface ProviderModalProps {
	/** null = alta; Provider = edición. */
	provider: Provider | null;
	onClose: () => void;
}

const ROLES: Provider["role"][] = ["review", "cheap", "embedding"];

export function ProviderModal({ provider, onClose }: ProviderModalProps) {
	const queryClient = useQueryClient();
	const [baseUrl, setBaseUrl] = useState(provider?.base_url ?? "");
	const [model, setModel] = useState(provider?.model ?? "");
	const [apiKey, setApiKey] = useState("");
	const [role, setRole] = useState<Provider["role"]>(
		provider?.role ?? "review",
	);
	const [priority, setPriority] = useState(String(provider?.priority ?? 0));

	const save = useMutation({
		mutationFn: () => {
			const body = {
				base_url: baseUrl.trim(),
				model: model.trim(),
				api_key: apiKey,
				role,
				priority: Number(priority) || 0,
				// Alta habilitada por defecto; en edición se conserva el flag.
				enabled: provider?.enabled ?? true,
			};
			return provider
				? api<Provider>(`/api/providers/${provider.id}`, {
						method: "PUT",
						body,
					})
				: api<Provider>("/api/providers", { method: "POST", body });
		},
		onSuccess: () => {
			queryClient.invalidateQueries({ queryKey: ["providers"] });
			onClose();
		},
	});

	const errorMessage =
		save.error instanceof ApiError
			? save.error.message
			: save.error
				? "No se pudo guardar el proveedor."
				: null;

	return (
		<Modal
			title={provider ? "Editar proveedor" : "Agregar proveedor"}
			onClose={onClose}
		>
			<form
				onSubmit={(event) => {
					event.preventDefault();
					save.mutate();
				}}
			>
				<Field label="Base URL" htmlFor="provider-base-url">
					<input
						id="provider-base-url"
						type="url"
						required
						placeholder="https://api.openai.com/v1"
						value={baseUrl}
						onInput={(event) =>
							setBaseUrl((event.target as HTMLInputElement).value)
						}
						class={inputClass}
					/>
				</Field>

				<Field label="Modelo" htmlFor="provider-model">
					<input
						id="provider-model"
						type="text"
						required
						value={model}
						onInput={(event) =>
							setModel((event.target as HTMLInputElement).value)
						}
						class={inputClass}
					/>
				</Field>

				<Field
					label={provider ? "API Key (vacía = conservar la actual)" : "API Key"}
					htmlFor="provider-api-key"
				>
					<input
						id="provider-api-key"
						type="password"
						required={!provider}
						placeholder={provider?.api_key}
						value={apiKey}
						onInput={(event) =>
							setApiKey((event.target as HTMLInputElement).value)
						}
						class={inputClass}
					/>
				</Field>

				<Field label="Rol" htmlFor="provider-role">
					<select
						id="provider-role"
						value={role}
						// onInput y no onChange: es la convención de Preact y evita la
						// normalización de preact/compat que quiebra el change del
						// select bajo jsdom (los inputs de texto ya usan onInput).
						onInput={(event) =>
							setRole(
								(event.target as HTMLSelectElement).value as Provider["role"],
							)
						}
						class={inputClass}
					>
						{ROLES.map((value) => (
							<option key={value} value={value}>
								{value}
							</option>
						))}
					</select>
				</Field>

				<Field label="Prioridad" htmlFor="provider-priority">
					<input
						id="provider-priority"
						type="number"
						step="1"
						value={priority}
						onInput={(event) =>
							setPriority((event.target as HTMLInputElement).value)
						}
						class={inputClass}
					/>
				</Field>

				{errorMessage && (
					<Banner tone="error" class="mb-3">
						{errorMessage}
					</Banner>
				)}

				<div class="flex justify-end gap-2">
					<Button type="button" variant="secondary" onClick={onClose}>
						Cancelar
					</Button>
					<Button type="submit" disabled={save.isPending}>
						{save.isPending ? "Guardando…" : "Guardar"}
					</Button>
				</div>
			</form>
		</Modal>
	);
}
