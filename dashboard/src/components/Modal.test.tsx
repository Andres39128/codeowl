/** Modal: cierre por ESC, por click en el fondo y no por click interno. */

import { cleanup, fireEvent, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Modal } from "./Modal";

afterEach(cleanup);

describe("Modal", () => {
	it("renderiza título y contenido", () => {
		render(
			<Modal title="Editar proveedor" onClose={() => {}}>
				<p>contenido</p>
			</Modal>,
		);

		expect(
			screen.getByRole("dialog", { name: "Editar proveedor" }),
		).toBeTruthy();
		expect(screen.getByText("contenido")).toBeTruthy();
	});

	it("cierra con ESC", () => {
		const onClose = vi.fn();
		render(
			<Modal title="Modal" onClose={onClose}>
				<p>x</p>
			</Modal>,
		);

		fireEvent.keyDown(document, { key: "Escape" });

		expect(onClose).toHaveBeenCalledTimes(1);
	});

	it("cierra con click en el fondo pero no con click interno", () => {
		const onClose = vi.fn();
		render(
			<Modal title="Modal" onClose={onClose}>
				<p>interno</p>
			</Modal>,
		);

		fireEvent.click(screen.getByTestId("modal-backdrop"));
		expect(onClose).toHaveBeenCalledTimes(1);

		fireEvent.click(screen.getByText("interno"));
		expect(onClose).toHaveBeenCalledTimes(1);
	});
});
