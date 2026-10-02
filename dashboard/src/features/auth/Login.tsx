/** Login del dashboard (guía §3.4): credenciales → POST /api/auth/login. */

import { useState } from "preact/hooks";
import { Button } from "../../components/Button";
import { ApiError } from "../../lib/apiClient";
import { useSession } from "../../lib/useSession";

export function Login() {
	const { login } = useSession();
	const [username, setUsername] = useState("");
	const [password, setPassword] = useState("");

	const onSubmit = (event: Event) => {
		event.preventDefault();
		login.mutate({ username, password });
	};

	const errorMessage =
		login.error instanceof ApiError
			? login.error.message
			: login.error
				? "No se pudo conectar con la API."
				: null;

	return (
		<main class="mx-auto flex min-h-screen max-w-sm flex-col justify-center px-4">
			<form
				onSubmit={onSubmit}
				class="rounded-lg border border-border-subtle bg-bg-surface p-6"
			>
				<h1 class="mb-4 text-xl font-semibold">codeowl</h1>

				<label class="block text-sm" for="login-username">
					Usuario
				</label>
				<input
					id="login-username"
					name="username"
					type="text"
					autocomplete="username"
					required
					value={username}
					onInput={(event) =>
						setUsername((event.target as HTMLInputElement).value)
					}
					class="mb-3 mt-1 w-full rounded-md border border-border-subtle bg-bg-elevated px-3 py-1.5 text-sm"
				/>

				<label class="block text-sm" for="login-password">
					Contraseña
				</label>
				<input
					id="login-password"
					name="password"
					type="password"
					autocomplete="current-password"
					required
					value={password}
					onInput={(event) =>
						setPassword((event.target as HTMLInputElement).value)
					}
					class="mb-3 mt-1 w-full rounded-md border border-border-subtle bg-bg-elevated px-3 py-1.5 text-sm"
				/>

				{errorMessage && (
					<p role="alert" class="mb-3 text-sm text-severity-alta">
						{errorMessage}
					</p>
				)}

				<Button type="submit" disabled={login.isPending} class="w-full">
					{login.isPending ? "Ingresando…" : "Iniciar sesión"}
				</Button>
			</form>
		</main>
	);
}
