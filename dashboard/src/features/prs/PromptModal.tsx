/** Creador de prompts por hallazgo (S1): arma un prompt con todo el
 * contexto (hallazgo, sugerencia, diff del archivo) listo para copiar y
 * pegar en el asistente de IA que el usuario prefiera. El dashboard no
 * llama a ningún servicio externo: el prompt viaja por el portapapeles. */

import { useState } from "preact/hooks";
import { Modal } from "../../components/Modal";
import type { FindingView } from "../../lib/types";

/** Extrae el fragmento del diff correspondiente al archivo (desde su
 * `diff --git` hasta el próximo). Límite de líneas: un archivo enorme no
 * puede volver inservible el prompt. */
export function hunkOf(diff: string, file: string): string {
	const marker = `diff --git a/${file} b/${file}`;
	const start = diff.indexOf(marker);
	if (start === -1) return "(el archivo no aparece en el diff)";
	const end = diff.indexOf("\ndiff --git ", start + 1);
	const hunk = end === -1 ? diff.slice(start) : diff.slice(start, end);
	const lines = hunk.split("\n");
	if (lines.length <= 120) return hunk;
	return `${lines.slice(0, 120).join("\n")}\n… (${lines.length - 120} líneas más)`;
}

const severityPlain: Record<string, string> = {
	high: "alta (puede romper algo importante)",
	medium: "media (conviene arreglarlo)",
	low: "baja (detalle menor)",
};

interface PromptModalProps {
	finding: FindingView;
	/** number presente = hallazgo de PR; ausente = hallazgo de un push
	 * directo (trazabilidad) — el prompt no inventa un número de PR. */
	repo: { owner: string; name: string; number?: number };
	diff: string;
	onClose: () => void;
}

/** Línea de contexto del repo: con PR nombra el pull request; para un push
 * directo lo dice honestamente (sin PR). */
function contextoDeRepo(repo: { owner: string; name: string; number?: number }): string {
	if (repo.number !== undefined && repo.number > 0) {
		return `Repositorio ${repo.owner}/${repo.name}, pull request #${repo.number}.`;
	}
	return `Repositorio ${repo.owner}/${repo.name}: cambios integrados directo a la rama base del repo (sin pull request).`;
}

/** Prompt de corrección: rol + contexto + qué pedir (el fix y la
 * explicación en lenguaje simple). */
export function promptCorregir(
	finding: FindingView,
	repo: { owner: string; name: string; number?: number },
	diff: string,
): string {
	const sev = severityPlain[finding.severity] ?? finding.severity;
	const origen =
		finding.source === "sast"
			? "un linter (análisis automático del código)"
			: "un modelo de lenguaje que leyó los cambios";
	const enPr = repo.number !== undefined && repo.number > 0;
	const partes = [
		"Sos un desarrollador senior. Una herramienta de revisión de código detectó un problema en un pull request y necesito que me ayudes a corregirlo. No sé programar: explicame todo en lenguaje simple.",
		"",
		`## El proyecto`,
		contextoDeRepo(repo),
		"",
		`## El problema detectado`,
		`- Archivo: ${finding.file}, línea ${finding.line}`,
		`- Severidad: ${sev}`,
		`- Detectado por: ${origen}`,
		`- Tipo de problema: ${finding.category}`,
		`- Descripción de la herramienta:`,
		"",
		finding.body,
		"",
	];
	if (finding.suggestion !== null && finding.suggestion !== "") {
		partes.push(
			"## Código corregido que propone la herramienta",
			"```",
			finding.suggestion,
			"```",
			"",
		);
	}
	partes.push(
		enPr
			? "## Fragmento del cambio en el PR (el diff del archivo)"
			: "## Fragmento del cambio (el diff del archivo)",
		"```diff",
		hunkOf(diff, finding.file),
		"```",
		"",
		"## Qué necesito de vos",
		"1. El código corregido, listo para reemplazar (solo la parte que cambia, claramente delimitada).",
		"2. Una explicación en lenguaje simple: qué estaba mal, por qué importaba y qué hace tu corrección.",
		"3. Los pasos exactos para aplicar el cambio en GitHub (dónde hacer clic, qué pegar).",
		"4. Si con esta información no alcanza para corregirlo, decime exactamente qué me falta pedirte.",
	);
	return partes.join("\n");
}

/** Prompt de explicación: entender la alerta sin código. */
export function promptExplicar(finding: FindingView): string {
	const sev = severityPlain[finding.severity] ?? finding.severity;
	return [
		"Explicame como si no supiera programar un hallazgo de una herramienta de revisión de código.",
		"",
		`El hallazgo es sobre el archivo ${finding.file}, línea ${finding.line}. Severidad: ${sev}.`,
		"",
		"Lo que dice la herramienta:",
		"",
		`"${finding.body}"`,
		"",
		"Contame:",
		"1. ¿Qué significa este problema en palabras simples?",
		"2. ¿Por qué puede ser un problema real?",
		"3. ¿Qué tanto me debería preocupar?",
		"4. ¿Cuál es el camino más simple para resolverlo si no sé programar?",
	].join("\n");
}

export function PromptModal({
	finding,
	repo,
	diff,
	onClose,
}: PromptModalProps) {
	const [modo, setModo] = useState<"corregir" | "explicar">("corregir");
	const [copiado, setCopiado] = useState(false);
	const prompt =
		modo === "corregir"
			? promptCorregir(finding, repo, diff)
			: promptExplicar(finding);

	async function copiar() {
		try {
			await navigator.clipboard.writeText(prompt);
			setCopiado(true);
			setTimeout(() => setCopiado(false), 2000);
		} catch {
			setCopiado(false);
		}
	}

	return (
		<Modal title="Arreglar con IA" onClose={onClose}>
			<div class="mb-3 flex gap-1" role="tablist">
				<button
					type="button"
					role="tab"
					aria-selected={modo === "corregir"}
					class={`rounded-md px-3 py-1.5 text-sm ${
						modo === "corregir"
							? "bg-bg-elevated font-medium text-text-primary"
							: "text-text-muted hover:text-text-primary"
					}`}
					onClick={() => setModo("corregir")}
				>
					Corregir
				</button>
				<button
					type="button"
					role="tab"
					aria-selected={modo === "explicar"}
					class={`rounded-md px-3 py-1.5 text-sm ${
						modo === "explicar"
							? "bg-bg-elevated font-medium text-text-primary"
							: "text-text-muted hover:text-text-primary"
					}`}
					onClick={() => setModo("explicar")}
				>
					Explicar
				</button>
			</div>
			<p class="mb-2 text-xs text-text-muted">
				Pegá este prompt en tu asistente de IA favorito (ChatGPT, Claude,
				Gemini…). Ya trae todo el contexto del hallazgo.
			</p>
			<pre class="max-h-96 overflow-auto rounded-md border border-border-subtle bg-bg-elevated p-3 text-xs whitespace-pre-wrap">
				<code>{prompt}</code>
			</pre>
			<div class="mt-3 flex justify-end gap-2">
				<button
					type="button"
					class="rounded border border-border-subtle px-3 py-1.5 text-sm text-text-muted hover:bg-bg-elevated"
					onClick={onClose}
				>
					Cerrar
				</button>
				<button
					type="button"
					class="rounded bg-action-primary px-3 py-1.5 text-sm font-medium text-bg-canvas hover:opacity-90"
					onClick={copiar}
				>
					{copiado ? "✓ Copiado" : "Copiar prompt"}
				</button>
			</div>
		</Modal>
	);
}
