/** Modal de invitación (§3.4): la invitación es member — los admins nacen del
 * seed del arranque — y la contraseña temporal viaja UNA sola vez en la
 * respuesta; el listado la muestra en un banner de una sola vista. */

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "preact/hooks";
import { Banner } from "../../components/Banner";
import { Button } from "../../components/Button";
import { Field, inputClass } from "../../components/Field";
import { Modal } from "../../components/Modal";
import { ApiError, api } from "../../lib/apiClient";

interface UserModalProps {
	onClose: () => void;
	onInvited: (invited: { username: string; password: string }) => void;
}

interface InviteResponse {
	username: string;
	temp_password: string;
}

export function UserModal({ onClose, onInvited }: UserModalProps) {
	const queryClient = useQueryClient();
	const [username, setUsername] = useState("");

	const invite = useMutation({
		mutationFn: () =>
			api<InviteResponse>("/api/users", {
				method: "POST",
				body: { username: username.trim() },
			}),
		onSuccess: (data) => {
			queryClient.invalidateQueries({ queryKey: ["users"] });
			onInvited({ username: data.username, password: data.temp_password });
			onClose();
		},
	});

	const errorMessage =
		invite.error instanceof ApiError
			? invite.error.message
			: invite.error
				? "No se pudo invitar al usuario."
				: null;

	return (
		<Modal title="Invitar miembro" onClose={onClose}>
			<form
				onSubmit={(event) => {
					event.preventDefault();
					invite.mutate();
				}}
			>
				<Field label="Usuario" htmlFor="invite-username">
					<input
						id="invite-username"
						type="text"
						required
						value={username}
						onInput={(event) =>
							setUsername((event.target as HTMLInputElement).value)
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
					<Button type="submit" disabled={invite.isPending}>
						{invite.isPending ? "Invitando…" : "Invitar"}
					</Button>
				</div>
			</form>
		</Modal>
	);
}
