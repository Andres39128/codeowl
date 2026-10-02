/** Alterna y persiste el tema claro/oscuro (guía §5.1). */

export type Theme = "light" | "dark";

/** Clave de persistencia; repetida inline en index.html (script anti-FOUC). */
export const THEME_STORAGE_KEY = "codeowl-theme";

/** Tema activo según el atributo data-theme de <html>. */
export function currentTheme(): Theme {
	return document.documentElement.dataset.theme === "dark" ? "dark" : "light";
}

/** Aplica el tema en <html> y lo persiste; devuelve el tema resultante. */
export function applyTheme(theme: Theme): Theme {
	document.documentElement.dataset.theme = theme;
	try {
		localStorage.setItem(THEME_STORAGE_KEY, theme);
	} catch {
		// localStorage bloqueado (modo privado): el toggle sigue funcionando,
		// solo no persiste entre recargas.
	}
	return theme;
}

/** Alterna claro ↔ oscuro, persiste y devuelve el tema resultante. */
export function toggleTheme(): Theme {
	return applyTheme(currentTheme() === "dark" ? "light" : "dark");
}
