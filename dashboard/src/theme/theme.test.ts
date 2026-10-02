/** Persistencia del toggle de tema (mapa: dashboard.theme + toggleTheme()). */

import { beforeEach, describe, expect, it } from "vitest";
import {
	applyTheme,
	currentTheme,
	THEME_STORAGE_KEY,
	toggleTheme,
} from "./theme";

beforeEach(() => {
	localStorage.clear();
	document.documentElement.dataset.theme = "light";
});

describe("toggleTheme", () => {
	it("alterna el tema en <html> y lo persiste en localStorage", () => {
		expect(toggleTheme()).toBe("dark");
		expect(document.documentElement.dataset.theme).toBe("dark");
		expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("dark");

		expect(toggleTheme()).toBe("light");
		expect(document.documentElement.dataset.theme).toBe("light");
		expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("light");
	});

	it("usa la clave canónica codeowl-theme", () => {
		expect(THEME_STORAGE_KEY).toBe("codeowl-theme");
		toggleTheme();
		expect(localStorage.getItem("codeowl-theme")).toBe("dark");
	});
});

describe("applyTheme / currentTheme", () => {
	it("fija un tema explícito y lo reporta", () => {
		expect(applyTheme("dark")).toBe("dark");
		expect(currentTheme()).toBe("dark");
		expect(applyTheme("light")).toBe("light");
		expect(currentTheme()).toBe("light");
	});
});
