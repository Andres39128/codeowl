/** Settings — usuarios (§3.4): invitación, reset de contraseña y baja por
 * flag (sin delete). La contraseña temporal se muestra UNA sola vez, en un
 * banner que el admin cierra o reemplaza. Exclusivo del admin. */

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "preact/hooks";
import { Badge } from "../../components/Badge";
import { Banner } from "../../components/Banner";
import { Button } from "../../components/Button";
import { type Column, Table } from "../../components/Table";
import { ApiError, api } from "../../lib/apiClient";
import type { ManagedUser } from "../../lib/types";
import { useSession } from "../../lib/useSession";
import { UserModal } from "./UserModal";

/** Contraseña temporal de una sola vista (§3.4): viaja solo en la respuesta
 * de invitar/resetear; no hay forma de volver a pedirla. */
interface TempPassword {
	username: string;
	password: string;
}

export function Users() {
	const queryClient = useQueryClient();
	const { user: sessionUser } = useSession();
	const users = useQuery({
		queryKey: ["users"],
		queryFn: () => api<ManagedUser[]>("/api/users"),
	});
	const [inviting, setInviting] = useState(false);
	const [tempPassword, setTempPassword] = useState<TempPassword | null>(null);

	const action = useMutation({
		mutationFn: ({
			id,
			action,
		}: {
			id: number;
			action: "reset_password" | "disable" | "enable";
		}) =>
			api<{ temp_password?: string }>(`/api/users/${id}`, {
				method: "PUT",
				body: { action },
			}),
		onSuccess: (data, variables) => {
			if (data.temp_password) {
				setTempPassword({
					username:
						users.data?.find((user) => user.id === variables.id)?.username ??
						"",
					password: data.temp_password,
				});
			}
			queryClient.invalidateQueries({ queryKey: ["users"] });
		},
	});

	const actionMessage =
		action.error instanceof ApiError
			? action.error.message
			: action.error
				? "La operación falló."
				: null;

	const columns: Column<ManagedUser>[] = [
		{ key: "username", header: "Usuario" },
		{
			key: "role",
			header: "Rol",
			render: (user) => <Badge>{user.role}</Badge>,
		},
		{
			key: "status",
			header: "Estado",
			render: (user) => (
				<span class="flex flex-wrap gap-1">
					{user.disabled ? (
						<Badge tone="alta">● Deshabilitado</Badge>
					) : (
						<Badge tone="baja">● Activo</Badge>
					)}
					{user.must_change_password && (
						<Badge tone="media">● Cambio de contraseña pendiente</Badge>
					)}
				</span>
			),
		},
		{
			key: "actions",
			header: "Acciones",
			render: (user) => {
				const isSelf = user.username === sessionUser?.username;
				return (
					<span class="flex gap-1">
						<Button
							variant="ghost"
							onClick={() =>
								action.mutate({ id: user.id, action: "reset_password" })
							}
						>
							Resetear contraseña
						</Button>
						{!isSelf &&
							(user.disabled ? (
								<Button
									variant="ghost"
									onClick={() =>
										action.mutate({ id: user.id, action: "enable" })
									}
								>
									Habilitar
								</Button>
							) : (
								// Auto-bloqueo oculto también en la UI (el backend lo
								// rechaza con 400, §3.4): nadie se echa a sí mismo.
								<Button
									variant="ghost"
									onClick={() =>
										action.mutate({ id: user.id, action: "disable" })
									}
								>
									Deshabilitar
								</Button>
							))}
					</span>
				);
			},
		},
	];

	return (
		<section>
			<div class="mb-4 flex items-center justify-between gap-4">
				<h1 class="text-xl font-semibold">Usuarios</h1>
				<Button onClick={() => setInviting(true)}>Invitar miembro</Button>
			</div>

			{tempPassword && (
				<Banner tone="success" class="mb-4">
					Contraseña temporal para <strong>{tempPassword.username}</strong>:{" "}
					<code class="font-mono">{tempPassword.password}</code> — se muestra
					una sola vez; entregála por fuera del sistema.
				</Banner>
			)}

			{actionMessage && (
				<Banner tone="error" class="mb-4">
					{actionMessage}
				</Banner>
			)}

			{users.isError ? (
				<Banner tone="error">No se pudieron cargar los usuarios.</Banner>
			) : users.isPending ? (
				<p class="text-text-muted">Cargando…</p>
			) : (
				<Table
					caption="Usuarios de la organización"
					columns={columns}
					rows={users.data}
					getRowKey={(user) => String(user.id)}
				/>
			)}

			{inviting && (
				<UserModal
					onClose={() => setInviting(false)}
					onInvited={setTempPassword}
				/>
			)}
		</section>
	);
}
