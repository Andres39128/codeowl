/** Routing hash puro y testeable: resuelve la ruta efectiva según el hash y el
 * rol (§3.4 — todo settings es exclusivo del admin; el member aterriza en la
 * cola). App corrige el hash cuando `redirect` viene no null. */

export type Route = "queue" | "providers" | "repos" | "users";

const ROUTE_HASH: Record<Route, string> = {
	queue: "#/queue",
	providers: "#/settings/providers",
	repos: "#/settings/repos",
	users: "#/settings/users",
};

const HASH_ROUTE: Record<string, Route> = {
	"#/queue": "queue",
	"#/settings/providers": "providers",
	"#/settings/repos": "repos",
	"#/settings/users": "users",
};

/** Pantalla de aterrizaje según rol: admin a proveedores, member a la cola. */
export function defaultRoute(isAdmin: boolean): Route {
	return isAdmin ? "providers" : "queue";
}

/** Resuelve el hash a la ruta a renderizar. Un hash desconocido o vedado para
 * el rol devuelve la ruta de fallback con su `redirect`. */
export function resolveRoute(
	hash: string,
	isAdmin: boolean,
): { route: Route; redirect: string | null } {
	const route = HASH_ROUTE[hash];
	if (route === undefined || (!isAdmin && route !== "queue")) {
		const fallback = defaultRoute(isAdmin);
		return { route: fallback, redirect: ROUTE_HASH[fallback] };
	}
	return { route, redirect: null };
}
