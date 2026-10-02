/** Layout: la sección Configuración solo es visible al admin (§3.4). */

import { cleanup, fireEvent, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Layout } from "./Layout";

afterEach(cleanup);

const base = {
	onLogout: () => {},
	logoutPending: false,
};

describe("Layout", () => {
	it("member: ve la cola y NO ve la sección de configuración", () => {
		render(
			<Layout
				{...base}
				user={{ username: "ana", role: "member", must_change_password: false }}
				route="queue"
			>
				<p>contenido</p>
			</Layout>,
		);

		expect(screen.getByText("Cola de Jobs")).toBeTruthy();
		expect(screen.queryByText("Configuración")).toBeNull();
		expect(screen.queryByText("Proveedores")).toBeNull();
	});

	it("admin: ve los tres links de settings", () => {
		render(
			<Layout
				{...base}
				user={{ username: "root", role: "admin", must_change_password: false }}
				route="providers"
			>
				<p>contenido</p>
			</Layout>,
		);

		for (const link of ["Proveedores", "Repos", "Usuarios"]) {
			expect(screen.getByText(link)).toBeTruthy();
		}
	});

	it("logout dispara el callback", () => {
		const onLogout = vi.fn();
		render(
			<Layout
				onLogout={onLogout}
				logoutPending={false}
				user={{ username: "root", role: "admin", must_change_password: false }}
				route="queue"
			>
				<p>contenido</p>
			</Layout>,
		);

		fireEvent.click(screen.getByRole("button", { name: "Salir" }));

		expect(onLogout).toHaveBeenCalledTimes(1);
	});
});
