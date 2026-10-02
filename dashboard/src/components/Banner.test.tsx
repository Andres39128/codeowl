/** Banner: tono → rol ARIA (alert para lo urgente, status para el resto). */

import { cleanup, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it } from "vitest";
import { Banner } from "./Banner";

afterEach(cleanup);

describe("Banner", () => {
	it("warning y error usan role=alert", () => {
		render(<Banner tone="warning">Cuidado</Banner>);
		expect(screen.getByRole("alert").textContent).toBe("Cuidado");
	});

	it("info y success usan role=status", () => {
		render(<Banner tone="success">Todo bien</Banner>);
		expect(screen.getByRole("status").textContent).toContain("Todo bien");
	});
});
