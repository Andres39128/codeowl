/** Card con testing-library (mapa: dashboard.components). */

import { cleanup, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it } from "vitest";
import { Card } from "./Card";

afterEach(cleanup);

describe("Card", () => {
	it("renderiza título e hijos", () => {
		render(
			<Card title="Detalle del PR">
				<p>Contenido</p>
			</Card>,
		);

		expect(
			screen.getByRole("heading", { name: "Detalle del PR" }),
		).toBeTruthy();
		expect(screen.getByText("Contenido")).toBeTruthy();
	});

	it("renderiza el slot de acciones junto al título", () => {
		render(
			<Card title="PR #12" actions={<button type="button">Revisar</button>}>
				<p>cuerpo</p>
			</Card>,
		);

		expect(screen.getByRole("button", { name: "Revisar" })).toBeTruthy();
		expect(screen.getByRole("heading", { name: "PR #12" })).toBeTruthy();
	});

	it("sin título ni acciones no renderiza cabecera", () => {
		const { container } = render(<Card>Solo contenido</Card>);

		expect(container.querySelector("h2")).toBeNull();
		expect(screen.getByText("Solo contenido")).toBeTruthy();
	});
});
