/** resolveRoute: aterrizaje por rol y guarda de settings (mapa: dashboard.lib). */

import { describe, expect, it } from "vitest";
import { defaultRoute, resolveRoute } from "./router";

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
});
