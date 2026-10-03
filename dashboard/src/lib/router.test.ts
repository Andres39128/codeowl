/** resolveRoute: aterrizaje por rol, guarda de settings y detalle de PR
 * (mapa: dashboard.lib). */

import { describe, expect, it } from "vitest";
import { defaultRoute, prIdFromHash, resolveRoute } from "./router";

describe("resolveRoute", () => {
	it("hash vacío o desconocido aterriza según rol", () => {
		expect(resolveRoute("#/", true)).toEqual({
			route: "providers",
			redirect: "#/settings/providers",
		});
		expect(resolveRoute("#/", false)).toEqual({
			route: "queue",
			redirect: "#/queue",
		});
		expect(resolveRoute("#/login", true).route).toBe("providers");
		expect(resolveRoute("#/cualquiera", false).redirect).toBe("#/queue");
	});

	it("member que navega a settings es redirigido a la cola (§3.4)", () => {
		for (const hash of [
			"#/settings/providers",
			"#/settings/repos",
			"#/settings/users",
		]) {
			expect(resolveRoute(hash, false)).toEqual({
				route: "queue",
				redirect: "#/queue",
			});
		}
	});

	it("admin accede a las tres secciones y member a la cola, sin redirect", () => {
		expect(resolveRoute("#/settings/providers", true)).toEqual({
			route: "providers",
			redirect: null,
		});
		expect(resolveRoute("#/settings/repos", true)).toEqual({
			route: "repos",
			redirect: null,
		});
		expect(resolveRoute("#/settings/users", true)).toEqual({
			route: "users",
			redirect: null,
		});
		expect(resolveRoute("#/queue", false)).toEqual({
			route: "queue",
			redirect: null,
		});
	});

	it("defaultRoute: admin → proveedores, member → cola", () => {
		expect(defaultRoute(true)).toBe("providers");
		expect(defaultRoute(false)).toBe("queue");
	});

	it("prs es visible a member y admin sin redirect (§3.4)", () => {
		expect(resolveRoute("#/prs", false)).toEqual({
			route: "prs",
			redirect: null,
		});
		expect(resolveRoute("#/prs", true)).toEqual({
			route: "prs",
			redirect: null,
		});
	});

	it("triage es visible a member y admin sin redirect (§6 F5)", () => {
		expect(resolveRoute("#/triage", false)).toEqual({
			route: "triage",
			redirect: null,
		});
		expect(resolveRoute("#/triage", true)).toEqual({
			route: "triage",
			redirect: null,
		});
	});

	it("detalle #/prs/<id> comparte la ruta prs; id inválido vuelve a la lista", () => {
		expect(prIdFromHash("#/prs/7")).toBe(7);
		expect(prIdFromHash("#/prs")).toBeNull();
		expect(prIdFromHash("#/prs/abc")).toBeNull();
		expect(prIdFromHash("#/prs/7/extra")).toBeNull();
		expect(resolveRoute("#/prs/42", false)).toEqual({
			route: "prs",
			redirect: null,
		});
		expect(resolveRoute("#/prs/abc", true)).toEqual({
			route: "prs",
			redirect: "#/prs",
		});
	});
});
