/** App F0: resuelve sesión → login o shell. */

import { useEffect } from "preact/hooks";
import { Login } from "./features/auth/Login";
import { useSession } from "./lib/useSession";
import { Shell } from "./Shell";

export function App() {
	const session = useSession();

	// Routing hash mínimo de F0, sin librería de router (guía §2.1): el hash
	// solo refleja el estado de sesión — #/login pública, #/ el shell. El
	// fallback SPA de la API (guía §3.5) permite migrar a path routing cuando
	// aparezca la segunda pantalla real.
	useEffect(() => {
		if (session.isLoading) return;
		const expected = session.user ? "#/" : "#/login";
		if (location.hash !== expected) location.hash = expected;
	}, [session.isLoading, session.user]);

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
	return (
		<Shell
			user={session.user}
			onLogout={() => session.logout.mutate()}
			logoutPending={session.logout.isPending}
		/>
	);
}
