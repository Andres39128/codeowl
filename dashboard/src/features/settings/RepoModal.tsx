/** Modal de conexión de repo. GitHub pide owner/name/external_id — los
 * secretos son globales de la App (§9.3); GitLab suma sus credenciales por
 * repo (webhook_secret, api_token, deploy_key y base_url self-managed), que
 * viajan en claro una sola vez y se cifran server-side (§9.2). */

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "preact/hooks";
import { Banner } from "../../components/Banner";
import { Button } from "../../components/Button";
import { Field, inputClass } from "../../components/Field";
import { Modal } from "../../components/Modal";
import { ApiError, api } from "../../lib/apiClient";
import type { Repo } from "../../lib/types";

interface RepoModalProps {
	onClose: () => void;
}

export function RepoModal({ onClose }: RepoModalProps) {
	const queryClient = useQueryClient();
	const [vcs, setVcs] = useState<Repo["vcs"]>("github");
	const [owner, setOwner] = useState("");
	const [name, setName] = useState("");
	const [externalId, setExternalId] = useState("");
	const [webhookSecret, setWebhookSecret] = useState("");
	const [apiToken, setApiToken] = useState("");
	const [deployKey, setDeployKey] = useState("");
	const [baseUrl, setBaseUrl] = useState("");

	const connect = useMutation({
		mutationFn: () =>
			api<Repo>("/api/repos", {
				method: "POST",
				// Los campos GitLab viajan vacíos en GitHub: el backend los ignora.
				body: {
					vcs,
					owner: owner.trim(),
					name: name.trim(),
					external_id: Number(externalId) || 0,
					webhook_secret: webhookSecret,
					api_token: apiToken,
					deploy_key: deployKey,
					base_url: baseUrl.trim(),
				},
			}),
		onSuccess: () => {
			queryClient.invalidateQueries({ queryKey: ["repos"] });
			onClose();
		},
	});

	const errorMessage =
		connect.error instanceof ApiError
			? connect.error.message
			: connect.error
				? "No se pudo conectar el repo."
				: null;

	return (
		<Modal title="Conectar repo" onClose={onClose}>
			<form
				onSubmit={(event) => {
					event.preventDefault();
					connect.mutate();
				}}
			>
				<Field label="VCS" htmlFor="repo-vcs">
					<select
						id="repo-vcs"
						value={vcs}
						// onInput y no onChange: convención de Preact; el change del
						// select no dispara bajo preact/compat + jsdom (§ ProviderModal).
						onInput={(event) =>
							setVcs((event.target as HTMLSelectElement).value as Repo["vcs"])
						}
						class={inputClass}
					>
						<option value="github">GitHub</option>
						<option value="gitlab">GitLab</option>
					</select>
				</Field>

				<Field label="Owner" htmlFor="repo-owner">
					<input
						id="repo-owner"
						type="text"
						required
						value={owner}
						onInput={(event) =>
							setOwner((event.target as HTMLInputElement).value)
						}
						class={inputClass}
					/>
				</Field>

				<Field label="Nombre del repo" htmlFor="repo-name">
					<input
						id="repo-name"
						type="text"
						required
						value={name}
						onInput={(event) =>
							setName((event.target as HTMLInputElement).value)
						}
						class={inputClass}
					/>
				</Field>

				<Field
					label="ID externo (external_id del repo en el VCS)"
					htmlFor="repo-external-id"
				>
					<input
						id="repo-external-id"
						type="number"
						step="1"
						min="1"
						required
						value={externalId}
						onInput={(event) =>
							setExternalId((event.target as HTMLInputElement).value)
						}
						class={inputClass}
					/>
				</Field>

				{vcs === "gitlab" && (
					<>
						<Field label="Webhook secret" htmlFor="repo-webhook-secret">
							<input
								id="repo-webhook-secret"
								type="password"
								required
								value={webhookSecret}
								onInput={(event) =>
									setWebhookSecret((event.target as HTMLInputElement).value)
								}
								class={inputClass}
							/>
						</Field>

						<Field
							label="API token (project access token, scope api)"
							htmlFor="repo-api-token"
						>
							<input
								id="repo-api-token"
								type="password"
								required
								value={apiToken}
								onInput={(event) =>
									setApiToken((event.target as HTMLInputElement).value)
								}
								class={inputClass}
							/>
						</Field>

						<Field label="Deploy key (clonado)" htmlFor="repo-deploy-key">
							<textarea
								id="repo-deploy-key"
								required
								rows={4}
								value={deployKey}
								onInput={(event) =>
									setDeployKey((event.target as HTMLTextAreaElement).value)
								}
								class={inputClass}
							/>
						</Field>

						<Field
							label="Base URL (self-managed; vacío = gitlab.com)"
							htmlFor="repo-base-url"
						>
							<input
								id="repo-base-url"
								type="url"
								placeholder="https://gitlab.miempresa.com"
								value={baseUrl}
								onInput={(event) =>
									setBaseUrl((event.target as HTMLInputElement).value)
								}
								class={inputClass}
							/>
						</Field>
					</>
				)}

				{errorMessage && (
					<Banner tone="error" class="mb-3">
						{errorMessage}
					</Banner>
				)}

				<div class="flex justify-end gap-2">
					<Button type="button" variant="secondary" onClick={onClose}>
						Cancelar
					</Button>
					<Button type="submit" disabled={connect.isPending}>
						{connect.isPending ? "Conectando…" : "Conectar"}
					</Button>
				</div>
			</form>
		</Modal>
	);
}
