/** DiffViewer: parser + render de texto plano (mapa: dashboard.components). */

import { cleanup, render, screen } from "@testing-library/preact";
import { afterEach, describe, expect, it } from "vitest";
import { DiffViewer, parseDiff } from "./DiffViewer";

afterEach(cleanup);

// Forma real de `git diff` / compare API: 2 archivos, 2 hunks en el primero,
// add/del/context, archivo borrado (/dev/null) y marcador de no-newline.
const FIXTURE = [
	"diff --git a/src/auth/login.go b/src/auth/login.go",
	"index 3f2a1bc..9c81de2 100644",
	"--- a/src/auth/login.go",
	"+++ b/src/auth/login.go",
	"@@ -1,4 +1,4 @@",
	" package auth",
	" ",
	"-func Login(user string) error {",
	"+func Login(user string, token string) error {",
	" \treturn nil",
	"@@ -10,2 +10,3 @@ func Logout() {",
	' \tlog.Println("logout")',
	"+_ = token",
	" }",
	"\\ No newline at end of file",
	"diff --git a/src/old.go b/src/old.go",
	"deleted file mode 100644",
	"index 0ea5e3a..0000000",
	"--- a/src/old.go",
	"+++ /dev/null",
	"@@ -1,1 +0,0 @@",
	"-package main",
].join("\n");

describe("parseDiff", () => {
	it("parsea multi-archivo con 2 hunks, add/del/context y numeración por hunk", () => {
		const files = parseDiff(FIXTURE);

		expect(files).toHaveLength(2);
		const [login, deleted] = files;
		expect(login?.file).toBe("src/auth/login.go");
		expect(login?.hunks).toHaveLength(2);

		const [hunk1, hunk2] = login?.hunks ?? [];
		expect(hunk1?.header).toBe("@@ -1,4 +1,4 @@");
		expect(hunk1?.oldStart).toBe(1);
		expect(hunk1?.newStart).toBe(1);
		expect(hunk1?.lines.map((l) => l.type)).toEqual([
			"context",
			"context",
			"del",
			"add",
			"context",
		]);
		expect(hunk1?.lines[2]).toEqual({
			type: "del",
			text: "func Login(user string) error {",
			oldLine: 3,
		});
		expect(hunk1?.lines[3]?.newLine).toBe(3);

		// Segundo hunk: la numeración arranca del header, no del hunk previo.
		expect(hunk2?.lines[1]).toEqual({
			type: "add",
			text: "_ = token",
			newLine: 11,
		});
		// "\ No newline at end of file" no genera fila.
		expect(hunk2?.lines).toHaveLength(3);

		expect(deleted?.file).toBe("src/old.go");
		expect(deleted?.hunks[0]?.newStart).toBe(0);
		expect(deleted?.hunks[0]?.lines[0]).toEqual({
			type: "del",
			text: "package main",
			oldLine: 1,
		});
	});

	it("diff vacío → []", () => {
		expect(parseDiff("")).toEqual([]);
		expect(parseDiff("  \n \n")).toEqual([]);
	});

	it("rename y binario → entrada de archivo sin hunks", () => {
		const files = parseDiff(
			[
				"diff --git a/docs/guia.md b/docs/guide.md",
				"similarity index 92%",
				"rename from docs/guia.md",
				"rename to docs/guide.md",
				"diff --git a/assets/logo.png b/assets/logo.png",
				"index 0ea5e3a..9c81de2 100644",
				"Binary files a/assets/logo.png and b/assets/logo.png differ",
			].join("\n"),
		);

		expect(files.map((f) => f.file)).toEqual([
			"docs/guide.md",
			"assets/logo.png",
		]);
		expect(files[0]?.hunks).toHaveLength(0);
		expect(files[1]?.hunks).toHaveLength(0);
	});

	it("entrada malformada no lanza y degrada a contexto", () => {
		const files = parseDiff(
			[
				"diff --git a/x.go b/x.go",
				"basura total",
				"@@ -1,1 +1,1 @@",
				"<<< rara >>>",
			].join("\n"),
		);

		expect(files).toHaveLength(1);
		// "basura total" está fuera de hunk: ignorada. "<<< rara >>>" dentro
		// de hunk: contexto defensivo sin numerar.
		expect(files[0]?.hunks[0]?.lines).toEqual([
			{ type: "context", text: "<<< rara >>>" },
		]);
	});
});

describe("DiffViewer", () => {
	it("renderiza archivos, hunks y filas con clases add/del", () => {
		const { container } = render(<DiffViewer diff={FIXTURE} />);

		expect(screen.getByText("src/auth/login.go")).toBeTruthy();
		expect(screen.getByText("src/old.go")).toBeTruthy();
		expect(screen.getByText("@@ -1,4 +1,4 @@")).toBeTruthy();

		const addRow = container.querySelector('[data-type="add"]');
		expect(addRow?.className).toContain("bg-diff-add");
		expect(container.querySelector('[data-type="del"]')?.className).toContain(
			"bg-diff-del",
		);
		// Los textos del diff se renderizan como filas reales.
		expect(
			screen.getByText("func Login(user string, token string) error {"),
		).toBeTruthy();
	});

	it("highlight ancla filas por archivo + línea (nuevo; del por vieja)", () => {
		const { container } = render(
			<DiffViewer
				diff={FIXTURE}
				highlights={[
					{ file: "src/auth/login.go", line: 3 },
					{ file: "src/old.go", line: 1 },
				]}
			/>,
		);

		const highlighted = [...container.querySelectorAll(".bg-diff-hl")];
		// login.go línea 3 matchea la fila del (oldLine 3) y la add (newLine 3);
		// old.go línea 1 matchea la única del del archivo borrado.
		expect(highlighted.map((row) => row.getAttribute("data-type"))).toEqual([
			"del",
			"add",
			"del",
		]);
		expect(
			highlighted.every(
				(row) =>
					row.className.includes("border-accent") &&
					row.className.includes("text-text-primary"),
			),
		).toBe(true);
		// El resto de las filas no queda marcada.
		expect(
			container.querySelectorAll('[data-type="context"].bg-diff-hl'),
		).toHaveLength(0);
	});

	it("diff vacío o basura total no renderiza nada", () => {
		const { container } = render(<DiffViewer diff="no es un diff" />);
		expect(container.firstChild).toBeNull();
	});

	it("HTML en el diff se renderiza como texto, jamás como markup (§9.5)", () => {
		const evil = [
			"diff --git a/x.go b/x.go",
			"@@ -1,1 +1,1 @@",
			"+<script>alert(1)</script>",
		].join("\n");
		render(<DiffViewer diff={evil} />);

		expect(screen.getByText("<script>alert(1)</script>")).toBeTruthy();
		expect(document.querySelector("script")).toBeNull();
	});
});
