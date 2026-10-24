/** Guía (S2): los cinco pasos en lenguaje llano, accesible para member. */

import { cleanup, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it } from "vitest";
import { Guia } from "./Guia";

describe("Guia", () => {
	afterEach(cleanup);

	it("trae los cinco pasos con vocabulario explicado", () => {
		render(<Guia />);

		expect(screen.getByText("1. Conectá tu repositorio")).toBeTruthy();
		expect(screen.getByText("2. Revisá la salud del repo")).toBeTruthy();
		expect(screen.getByText("3. Leé las alertas (hallazgos)")).toBeTruthy();
		expect(
			screen.getByText("4. Corregí con la IA (Arreglar con IA)"),
		).toBeTruthy();
		expect(screen.getByText("5. Verificá que quedó arreglado")).toBeTruthy();
		// Jerga traducida, no asumida.
		expect(screen.getByText(/puede romper algo importante/)).toBeTruthy();
		expect(
			screen.getByText(/listo para pegar en el asistente de IA/),
		).toBeTruthy();
		expect(screen.getByText(/No aplica/)).toBeTruthy();
	});

	it("linkea a las pantallas de la herramienta", () => {
		render(<Guia />);

		const links = [...document.querySelectorAll("a")].map((a) =>
			a.getAttribute("href"),
		);
		expect(links).toContain("#/settings/repos");
		expect(links).toContain("#/prs");
		expect(links).toContain("#/queue");
		expect(links).toContain("#/settings/providers");
	});
});
