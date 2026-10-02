import preact from "@preact/preset-vite";
import tailwindcss from "@tailwindcss/vite";
import { defineConfig } from "vitest/config";

// El preset ya aliasa react → preact/compat (guía §2.1): TanStack Query y el
// ecosistema React corren sobre Preact sin tocar nada más.
//
// Proxy en dev: el browser habla http same-origin con Vite y Vite reenvía a
// la API en :8080 — la cookie Secure viaja igual (localhost es origen
// confiable para los browsers) y no hay CORS. En prod la API sirve los
// estáticos con el mismo origen (guía §3.5), detrás de Caddy con TLS.
export default defineConfig({
	plugins: [preact(), tailwindcss()],
	server: {
		proxy: {
			"/api": "http://localhost:8080",
			"/healthz": "http://localhost:8080",
		},
	},
	test: {
		environment: "jsdom",
		server: {
			deps: {
				// Sin inline, vitest externaliza node_modules y el alias del preset
				// (react → preact/compat) no aplica a TanStack Query.
				inline: ["@tanstack/react-query"],
			},
		},
	},
});
