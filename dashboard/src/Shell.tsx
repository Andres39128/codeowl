/** Shell F0: navegación vacía — marca, toggle de tema, usuario y logout. */

import { useState } from "preact/hooks";
import { Button } from "./components/Button";
import type { User } from "./lib/useSession";
import { type Theme, toggleTheme } from "./theme/theme";

interface ShellProps {
	user: User;
	onLogout: () => void;
	logoutPending: boolean;
}

export function Shell({ user, onLogout, logoutPending }: ShellProps) {
	const [theme, setTheme] = useState<Theme>(() =>
		document.documentElement.dataset.theme === "dark" ? "dark" : "light",
	);

	return (
		<div class="min-h-screen">
			<nav class="flex items-center justify-between border-b border-border-subtle bg-bg-elevated px-4 py-2">
				<span class="font-semibold">codeowl</span>
				<div class="flex items-center gap-3 text-sm">
					<Button
						variant="ghost"
						onClick={() => setTheme(toggleTheme())}
						aria-label={`Cambiar a modo ${theme === "dark" ? "claro" : "oscuro"}`}
					>
						Modo {theme === "dark" ? "claro" : "oscuro"}
					</Button>
					<span class="text-text-muted">
						{user.username} · {user.role}
					</span>
					<Button
						variant="secondary"
						onClick={onLogout}
						disabled={logoutPending}
					>
						Salir
					</Button>
				</div>
			</nav>
			<main class="mx-auto max-w-5xl p-6">
				<p class="text-text-muted">F0 — sin módulos</p>
			</main>
		</div>
	);
}
