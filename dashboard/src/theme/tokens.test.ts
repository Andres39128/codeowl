/** Snapshot de tokens + contraste AA en ambos temas (mapa: dashboard.theme). */

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// tokens.css es la fuente única de valores: el test lee el archivo real.
// (?raw queda vacío en vitest por el plugin de Tailwind; import.meta.url no
// es file:// en vitest — se resuelve desde la raíz del paquete, el cwd de
// `pnpm --dir dashboard test`.)
const css = readFileSync("src/theme/tokens.css", "utf8");
function varsOf(blockRegex: RegExp): Record<string, string> {
	const block = css.match(blockRegex)?.[1] ?? "";
	const vars: Record<string, string> = {};
	for (const match of block.matchAll(/--([a-z-]+):\s*(#[0-9a-fA-F]{6})/g)) {
		const name = match[1];
		const value = match[2];
		if (!name || !value) continue;
		vars[name] = value.toLowerCase();
	}
	return vars;
}

function tokenOf(tokens: Record<string, string>, name: string): string {
	const value = tokens[name];
	if (value === undefined) throw new Error(`token faltante: ${name}`);
	return value;
}

// Tabla exacta de §5.1 de la guía — el snapshot falla si un token cambia.
const lightExpected = {
	"bg-base": "#fdf6ec",
	"bg-surface": "#faf0df",
	"bg-elevated": "#ffffff",
	"text-primary": "#16341f",
	"text-muted": "#4a6b58",
	"action-primary": "#2e6b4f",
	"action-primary-text": "#ffffff",
	"border-subtle": "#8fbf9f",
	accent: "#c2611a",
	"severity-alta": "#b3261e",
	"severity-media": "#92400e",
	"severity-baja": "#1e6b3c",
};
const darkExpected = {
	"bg-base": "#0e1f16",
	"bg-surface": "#142b1d",
	"bg-elevated": "#1c3a28",
	"text-primary": "#f2e8d8",
	"text-muted": "#a8c4b2",
	"action-primary": "#8fbf9f",
	"action-primary-text": "#0e1f16",
	"border-subtle": "#2e4a3a",
	accent: "#e8853d",
	"severity-alta": "#f87171",
	"severity-media": "#fbbf24",
	"severity-baja": "#4ade80",
};

function luminance(hex: string): number {
	const [r = 0, g = 0, b = 0] = [1, 3, 5].map((i) => {
		const channel = Number.parseInt(hex.slice(i, i + 2), 16) / 255;
		return channel <= 0.03928
			? channel / 12.92
			: ((channel + 0.055) / 1.055) ** 2.4;
	});
	return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(foreground: string, background: string): number {
	const l1 = luminance(foreground);
	const l2 = luminance(background);
	const [lighter, darker] = l1 >= l2 ? [l1, l2] : [l2, l1];
	return (lighter + 0.05) / (darker + 0.05);
}

const themes = [
	{ name: "claro", tokens: varsOf(/:root \{([^}]*)\}/) },
	{ name: "oscuro", tokens: varsOf(/\[data-theme="dark"\] \{([^}]*)\}/) },
];

describe("tokens.css — snapshot de §5.1", () => {
	it("claro: los 12 roles con el valor exacto de la guía", () => {
		expect(themes[0]?.tokens).toEqual(lightExpected);
	});

	it("oscuro: los 12 roles con el valor exacto de la guía", () => {
		expect(themes[1]?.tokens).toEqual(darkExpected);
	});
});

describe("contraste WCAG 2.1 AA en ambos temas (guía §5.2)", () => {
	const backgrounds = ["bg-base", "bg-surface", "bg-elevated"];
	const texts = [
		"text-primary",
		"text-muted",
		"severity-alta",
		"severity-media",
		"severity-baja",
	];

	for (const { name, tokens } of themes) {
		it(`${name}: todo texto sobre todo fondo ≥ 4.5:1`, () => {
			for (const bg of backgrounds) {
				for (const fg of texts) {
					expect(
						contrast(tokenOf(tokens, fg), tokenOf(tokens, bg)),
					).toBeGreaterThanOrEqual(4.5);
				}
			}
			expect(
				contrast(
					tokenOf(tokens, "action-primary-text"),
					tokenOf(tokens, "action-primary"),
				),
			).toBeGreaterThanOrEqual(4.5);
		});

		it(`${name}: accent ≥ 3:1 como indicador no-texto`, () => {
			expect(
				contrast(tokenOf(tokens, "accent"), tokenOf(tokens, "bg-base")),
			).toBeGreaterThanOrEqual(3);
			expect(
				contrast(tokenOf(tokens, "accent"), tokenOf(tokens, "bg-surface")),
			).toBeGreaterThanOrEqual(3);
		});
	}
});
