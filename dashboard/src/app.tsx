/** App F1: sesión → login o shell con routing hash. #/ aterriza según rol
 * (admin → proveedores, member → cola); settings es exclusivo del admin y un
 * member que navega a #/settings/* es redirigido a la cola (§3.4). La lista
 * de PRs es visible a member y admin; #/prs/<id> renderiza el detalle (F3). */

import { useEffect } from "preact/hooks";
import { Layout } from "./components/Layout";
import { Login } from "./features/auth/Login";
import { PrDetail } from "./features/prs/PrDetail";
import { Prs } from "./features/prs/Prs";
import { Queue } from "./features/queue/Queue";
import { Providers } from "./features/settings/Providers";
import { Repos } from "./features/settings/Repos";
import { Users } from "./features/settings/Users";
import { prIdFromHash, resolveRoute } from "./lib/router";
import { useHashRoute } from "./lib/useHashRoute";
import { useSession } from "./lib/useSession";

// Routing hash sin librería (guía §2.1): el fallback SPA de la API (§3.5)
// permite migrar a path routing cuando haga falta.
const PAGES = {
	queue: Queue,
	prs: Prs,
	providers: Providers,
	repos: Repos,
	users: Users,
} as const;

export function App() {
	const session = useSession();
	const hash = useHashRoute();
	const isAdmin = session.user?.role === "admin";

	useEffect(() => {
		if (session.isLoading || session.error) return;
		const target = session.user
			? resolveRoute(hash, isAdmin).redirect
			: "#/login";
		if (target && location.hash !== target) location.hash = target;
	}, [session.isLoading, session.error, session.user, isAdmin, hash]);

	if (session.isLoading) {
		return <p class="p-6 text-text-muted">Cargando…</p>;
	}
	if (session.error) {
		return (
			<p role="alert" class="p-6 text-severity-alta">
				No se pudo conectar con la API.
			</p>
		);
	}
	if (!session.user) {
		return <Login />;
	}

	const route = resolveRoute(hash, isAdmin).route;
	const Page = PAGES[route];
	// El detalle de PR comparte la ruta prs con el id en el hash (#/prs/<id>);
	// un id inválido ya viene redirigido a la lista por resolveRoute.
	const prId = prIdFromHash(hash);

	return (
		<Layout
			user={session.user}
			route={route}
			onLogout={() => session.logout.mutate()}
			logoutPending={session.logout.isPending}
		>
			{route === "prs" && prId !== null ? <PrDetail prId={prId} /> : <Page />}
		</Layout>
	);
}
