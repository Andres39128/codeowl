// eslint.config.mjs — config propio de la imagen (§9.4): los configs del repo
// revisado se ignoran; un eslint.config.js de repo es JS ejecutable en el
// sandbox y además permitiría silenciar la propia detección.
import js from "@eslint/js";
import tsPlugin from "@typescript-eslint/eslint-plugin";
import tsParser from "@typescript-eslint/parser";

// Globals estándar para archivos JS sueltos (browser + node + Web API): sin
// esto, `document`, `localStorage` o `fetch` disparan no-undef en repos
// frontend y el ruido sepulta los hallazgos reales. Curado a mano en vez de
// importar el paquete `globals` para no sumar una dependencia transitiva al
// pinning de la imagen (§9.4: salida reproducible entre rebuilds).
const jsGlobals = {
	// Node (CommonJS incluido): un script de repo puede usar require.
	require: "readonly",
	module: "readonly",
	exports: "readonly",
	__dirname: "readonly",
	__filename: "readonly",
	Buffer: "readonly",
	// Browser + Web API.
	console: "readonly",
	document: "readonly",
	fetch: "readonly",
	localStorage: "readonly",
	location: "readonly",
	navigator: "readonly",
	process: "readonly",
	sessionStorage: "readonly",
	setTimeout: "readonly",
	clearTimeout: "readonly",
	setInterval: "readonly",
	clearInterval: "readonly",
	URL: "readonly",
	URLSearchParams: "readonly",
	AbortController: "readonly",
	TextEncoder: "readonly",
	TextDecoder: "readonly",
	FormData: "readonly",
	WebSocket: "readonly",
	Worker: "readonly",
};

export default [
	// recommended primero: en flat config lo último pisa lo anterior — los
	// overrides por tipo de archivo van después de la base.
	js.configs.recommended,
	// Parser de TS para archivos TypeScript (el core no los parsea).
	{
		files: ["**/*.ts", "**/*.tsx", "**/*.mts", "**/*.cts"],
		plugins: { "@typescript-eslint": tsPlugin },
		languageOptions: {
			parser: tsParser,
			parserOptions: { ecmaFeatures: { jsx: true } },
		},
		rules: {
			// no-undef apagado para TS: el compilador de TypeScript ya valida
			// identificadores indefinidos y los globals de tipo (RequestInit,
			// HTMLInputElement) no se pueden enumerar como valores de runtime —
			// recomendación canónica de typescript-eslint.
			"no-undef": "off",
			// no-unused-vars del core no entiende sintaxis TS: marca parámetros
			// de firmas de tipos (interface con callbacks) como "sin usar". La
			// versión del plugin sí los distingue y detecta los reales.
			"no-unused-vars": "off",
			"@typescript-eslint/no-unused-vars": "error",
		},
	},
	// JS: recommended + globals estándar (el conjunto de arriba).
	{
		files: ["**/*.js", "**/*.mjs", "**/*.cjs"],
		languageOptions: { globals: jsGlobals },
	},
];
