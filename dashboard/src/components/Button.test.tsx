/** Button con testing-library (mapa: dashboard.components). */

import { cleanup, fireEvent, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Button } from "./Button";

afterEach(cleanup);

describe("Button", () => {
	it("renderiza la etiqueta y dispara onClick", () => {
		const onClick = vi.fn();
		render(<Button onClick={onClick}>Guardar</Button>);

		fireEvent.click(screen.getByRole("button", { name: "Guardar" }));

		expect(onClick).toHaveBeenCalledTimes(1);
	});

	it("disabled se propaga al botón", () => {
		render(<Button disabled>Guardar</Button>);

		// El bloqueo del click en disabled es nativo del browser; acá se verifica
		// el cableado del prop (jsdom dispara listeners sintéticos igual).
		expect(
			(screen.getByRole("button", { name: "Guardar" }) as HTMLButtonElement)
				.disabled,
		).toBe(true);
	});
});
