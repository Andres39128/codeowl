/** Shell del dashboard: sidebar con navegación, marca, toggle de tema, usuario
 * y logout (reutiliza la identidad del shell F0). §3.4: la sección de
 * configuración solo se muestra al admin; cola, triage y PRs son de todo
 * usuario. */

import type { ComponentChildren } from "preact";
import { useState } from "preact/hooks";
import type { Route } from "../lib/router";
import type { User } from "../lib/useSession";
import { type Theme, toggleTheme } from "../theme/theme";
import { Button } from "./Button";

interface LayoutProps {
	user: User;
	route: Route;
	onLogout: () => void;
	logoutPending: boolean;
	children: ComponentChildren;
}

const linkClass = (active: boolean) =>
	`block rounded-md px-3 py-1.5 text-sm ${
		active
			? "bg-bg-surface font-medium text-text-primary"
			: "text-text-muted hover:text-text-primary"
	}`;

const ariaCurrent = (active: boolean) => (active ? "page" : undefined);

export function Layout({
	user,
	route,
	onLogout,
	logoutPending,
	children,
}: LayoutProps) {
	const [theme, setTheme] = useState<Theme>(() =>
		document.documentElement.dataset.theme === "dark" ? "dark" : "light",
	);
	const isAdmin = user.role === "admin";

	return (
		<div class="flex min-h-screen">
			<aside class="flex w-56 shrink-0 flex-col border-r border-border-subtle bg-bg-elevated p-4">
				<span class="text-lg font-semibold">codeowl</span>

				<nav class="mt-6 flex flex-col gap-1" aria-label="Navegación principal">
					<a
						href="#/queue"
						aria-current={ariaCurrent(route === "queue")}
						class={linkClass(route === "queue")}
					>
						Cola de Jobs
					</a>

					<a
						href="#/triage"
						aria-current={ariaCurrent(route === "triage")}
						class={linkClass(route === "triage")}
					>
						Triage
					</a>

					<a
						href="#/prs"
						aria-current={ariaCurrent(route === "prs")}
						class={linkClass(route === "prs")}
					>
						PRs
					</a>

					{isAdmin && (
						<>
							<p class="mt-4 px-3 text-xs uppercase tracking-wide text-text-muted">
								Configuración
							</p>
							<a
								href="#/settings/providers"
								aria-current={ariaCurrent(route === "providers")}
								class={linkClass(route === "providers")}
							>
								Proveedores
							</a>
							<a
								href="#/settings/repos"
								aria-current={ariaCurrent(route === "repos")}
								class={linkClass(route === "repos")}
							>
								Repos
							</a>
							<a
								href="#/settings/users"
								aria-current={ariaCurrent(route === "users")}
								class={linkClass(route === "users")}
							>
								Usuarios
							</a>
						</>
					)}
				</nav>

				<div class="mt-auto flex flex-col gap-2 pt-4 text-sm">
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
			</aside>

			<main class="flex-1 p-6">{children}</main>
		</div>
	);
}
