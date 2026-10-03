/** DiffViewer (guía §5.3): renderiza un diff unificado multi-archivo como
 * TEXTO plano — contenido de VCS, el escaping es el nativo de Preact, jamás
 * HTML crudo ni innerHTML (guía §9.5). El parser es puro y tolerante: entrada
 * malformada se degrada a filas de contexto sin numerar, nunca lanza.
 * `highlights` ancla findings por archivo + línea (lado nuevo; las filas
 * eliminadas matchean por línea vieja) con borde accent + fondo propio. */

export type DiffLineType = "context" | "add" | "del";

export interface DiffLine {
	type: DiffLineType;
	text: string;
	oldLine?: number;
	newLine?: number;
}

export interface DiffHunk {
	header: string;
	oldStart: number;
	newStart: number;
	lines: DiffLine[];
}

export interface DiffFile {
	file: string;
	hunks: DiffHunk[];
}

/** Ancla de un finding sobre el diff: ruta del lado nuevo + número de línea. */
export interface DiffHighlight {
	file: string;
	line: number;
}

interface DiffViewerProps {
	diff: string;
	highlights?: DiffHighlight[];
}

const HUNK_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

/** Mejor esfuerzo para la ruta del lado nuevo en `diff --git a/x b/x`:
 * soporta forma citada de git (`"a/p a.txt" "b/p b.txt"`) y rutas con
 * espacios sin citar (último " b/" gana). */
function headerPath(rest: string): string {
	const quoted = /^"a\/(.*)" "b\/(.*)"$/.exec(rest);
	if (quoted?.[2] !== undefined) return quoted[2].replace(/\\(.)/g, "$1");
	const plain = / b\/(.*)$/.exec(rest);
	if (plain?.[1] !== undefined) return plain[1];
	return rest.split(" ")[1] ?? rest;
}

/**
 * Parsea un diff unificado (salida de git compare / GitHub / GitLab
 * rearmado) en archivos → hunks → filas. Defensivo por diseño: es dato
 * adyacente a LLM/VCS, una línea desconocida dentro de un hunk se vuelve
 * fila de contexto sin numerar en vez de romper el render.
 */
export function parseDiff(diff: string): DiffFile[] {
	if (diff.trim() === "") return [];

	const files: DiffFile[] = [];
	let current: DiffFile | null = null;
	let hunk: DiffHunk | null = null;
	let oldNo = 0;
	let newNo = 0;

	for (const raw of diff.split("\n")) {
		const line = raw.endsWith("\r") ? raw.slice(0, -1) : raw;

		if (line.startsWith("diff --git ")) {
			current = {
				file: headerPath(line.slice("diff --git ".length)),
				hunks: [],
			};
			files.push(current);
			hunk = null;
			continue;
		}
		if (current === null) continue; // basura antes del primer header

		if (line.startsWith("@@ ")) {
			const match = HUNK_RE.exec(line);
			if (match === null) continue; // @@ ilegible: se ignora, no lanza
			hunk = {
				header: line,
				oldStart: Number(match[1]),
				newStart: Number(match[3]),
				lines: [],
			};
			current.hunks.push(hunk);
			oldNo = Number(match[1]);
			newNo = Number(match[3]);
			continue;
		}

		if (hunk !== null) {
			if (line.startsWith("\\")) continue; // "\ No newline at end of file"
			const marker = line[0];
			if (marker === "+") {
				hunk.lines.push({ type: "add", text: line.slice(1), newLine: newNo++ });
				continue;
			}
			if (marker === "-") {
				hunk.lines.push({ type: "del", text: line.slice(1), oldLine: oldNo++ });
				continue;
			}
			if (marker === " ") {
				hunk.lines.push({
					type: "context",
					text: line.slice(1),
					oldLine: oldNo++,
					newLine: newNo++,
				});
				continue;
			}
			// Línea desconocida dentro del hunk: contexto defensivo sin numerar.
			hunk.lines.push({ type: "context", text: line });
			continue;
		}

		// Metadatos entre header y primer hunk.
		if (line.startsWith("+++ ")) {
			// El path confiable viene acá; git agrega \t y cita rutas con espacios.
			let path = line.slice(4).replace(/\t.*$/, "").trim();
			if (path.startsWith('"') && path.endsWith('"')) {
				path = path.slice(1, -1).replace(/\\(.)/g, "$1");
			}
			if (path === "/dev/null") continue; // archivo borrado: queda el b/ del header
			if (path.startsWith("b/")) path = path.slice(2);
			if (path !== "") current.file = path;
			continue;
		}
		if (line.startsWith("rename to ")) {
			current.file = line.slice("rename to ".length);
		}
		// index / old mode / new mode / --- / similarity / Binary files …: ruido.
	}
	return files;
}

const TYPE_MARK: Record<DiffLineType, string> = {
	context: " ",
	add: "+",
	del: "-",
};

const rowClasses: Record<DiffLineType, string> = {
	context: "text-text-primary",
	add: "bg-diff-add text-severity-baja",
	del: "bg-diff-del text-severity-alta",
};

export function DiffViewer({ diff, highlights }: DiffViewerProps) {
	const files = parseDiff(diff);
	if (files.length === 0) return null;

	const anchors = new Set(
		(highlights ?? []).map((h) => `${h.file}\u0000${h.line}`),
	);

	return (
		// max-h + scroll visible: el diff nunca se trunca en silencio.
		<div class="max-h-96 overflow-auto rounded-lg border border-border-subtle font-mono text-xs leading-5">
			{files.map((file) => (
				<div key={file.file}>
					<div class="sticky top-0 z-10 border-b border-border-subtle bg-bg-elevated px-3 py-1.5 font-sans text-sm font-medium">
						{file.file}
					</div>
					{file.hunks.map((hunk) => (
						<div key={hunk.header}>
							<div class="bg-bg-surface px-3 py-1 text-text-muted">
								{hunk.header}
							</div>
							{hunk.lines.map((line, index) => {
								const highlighted =
									(line.newLine !== undefined &&
										anchors.has(`${file.file}\u0000${line.newLine}`)) ||
									(line.type === "del" &&
										line.oldLine !== undefined &&
										anchors.has(`${file.file}\u0000${line.oldLine}`));
								return (
									<div
										// String(index) como en Table: filas posicionales.
										key={String(index)}
										data-file={file.file}
										data-line={line.newLine ?? line.oldLine}
										data-type={line.type}
										class={`min-h-5 flex border-l-2 ${
											highlighted
												? "border-accent bg-diff-hl text-text-primary"
												: `border-transparent ${rowClasses[line.type]}`
										}`}
									>
										<span class="w-10 shrink-0 pr-2 text-right text-text-muted select-none">
											{line.oldLine ?? ""}
										</span>
										<span class="w-10 shrink-0 pr-2 text-right text-text-muted select-none">
											{line.newLine ?? ""}
										</span>
										<span class="w-4 shrink-0 select-none whitespace-pre">
											{TYPE_MARK[line.type]}
										</span>
										<span class="pr-3 whitespace-pre">{line.text}</span>
									</div>
								);
							})}
						</div>
					))}
				</div>
			))}
		</div>
	);
}
