/** Routing hash puro y testeable: resuelve la ruta efectiva según el hash y el
 * rol (§3.4 — todo settings es exclusivo del admin; el member aterriza en la
 * cola y consulta triage, PRs y trazabilidad). El detalle de PR (#/prs/<id>)
 * comparte la ruta `prs` con el id como parámetro; un id inválido vuelve a la
 * lista. Ídem el detalle de push directo (#/trazabilidad/<id>, trazabilidad
 * S1). App corrige el hash cuando `redirect` viene no null. */

export type Route =
	| "queue"
	| "triage"
	| "prs"
	| "trazabilidad"
	| "providers"
	| "repos"
	| "users"
	| "guia";

const ROUTE_HASH: Record<Route, string> = {
	queue: "#/queue",
	triage: "#/triage",
	prs: "#/prs",
	trazabilidad: "#/trazabilidad",
	providers: "#/settings/providers",
	repos: "#/settings/repos",
	users: "#/settings/users",
	guia: "#/guia",
};

const HASH_ROUTE: Record<string, Route> = {
	"#/queue": "queue",
	"#/triage": "triage",
	"#/prs": "prs",
	"#/trazabilidad": "trazabilidad",
	"#/settings/providers": "providers",
	"#/settings/repos": "repos",
	"#/settings/users": "users",
	"#/guia": "guia",
};

/** Pantalla de aterrizaje según rol: admin a proveedores, member a la cola. */
export function defaultRoute(isAdmin: boolean): Route {
	return isAdmin ? "providers" : "queue";
}

/** Id de PR de un hash de detalle (`#/prs/<id>`): null si el hash no es un
 * detalle con id numérico. */
export function prIdFromHash(hash: string): number | null {
	const match = /^#\/prs\/(\d+)$/.exec(hash);
	return match === null ? null : Number(match[1]);
}

/** Id de push directo de un hash de detalle (`#/trazabilidad/<id>`): null si
 * el hash no es un detalle con id numérico (trazabilidad-merges S1). */
export function pushIdFromHash(hash: string): number | null {
	const match = /^#\/trazabilidad\/(\d+)$/.exec(hash);
	return match === null ? null : Number(match[1]);
}

/** Resuelve el hash a la ruta a renderizar. Un hash desconocido o vedado para
 * el rol devuelve la ruta de fallback con su `redirect`. */
export function resolveRoute(
	hash: string,
	isAdmin: boolean,
): { route: Route; redirect: string | null } {
	const route = HASH_ROUTE[hash];
	if (route === undefined) {
		// Detalle de PR: mismo route `prs`, el id viaja en el hash.
		if (prIdFromHash(hash) !== null) return { route: "prs", redirect: null };
		if (hash.startsWith("#/prs/")) return { route: "prs", redirect: "#/prs" };
		// Detalle de push directo (trazabilidad S1): mismo patrón.
		if (pushIdFromHash(hash) !== null) {
			return { route: "trazabilidad", redirect: null };
		}
		if (hash.startsWith("#/trazabilidad/")) {
			return { route: "trazabilidad", redirect: "#/trazabilidad" };
		}
	} else if (
		isAdmin ||
		route === "queue" ||
		route === "triage" ||
		route === "prs" ||
		route === "trazabilidad" ||
		route === "guia"
	) {
		return { route, redirect: null };
	}
	const fallback = defaultRoute(isAdmin);
	return { route: fallback, redirect: ROUTE_HASH[fallback] };
}
