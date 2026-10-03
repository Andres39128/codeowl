/** Mapas de etiqueta/tono compartidos por la lista y el detalle de PR
 * (mismo criterio que features/queue): el estado nunca se comunica solo con
 * color — el texto acompaña al tono (§5.2.3). Los lookups incluyen fallback
 * defensivo: dato fuera del conjunto cerrado se muestra crudo en neutral. */

import type { BadgeTone } from "../../components/Badge";
import type { FindingView, PrView, ReviewStatus } from "../../lib/types";

interface StatusView {
	label: string;
	tone: BadgeTone;
}

const stateView: Record<PrView["state"], StatusView> = {
	open: { label: "Abierto", tone: "baja" },
	closed: { label: "Cerrado", tone: "neutral" },
};

const reviewStatusView: Record<ReviewStatus, StatusView> = {
	running: { label: "En ejecución", tone: "media" },
	success: { label: "Exitosa", tone: "baja" },
	partial: { label: "Parcial", tone: "media" },
	stale: { label: "Desactualizada", tone: "neutral" },
	failed: { label: "Fallida", tone: "alta" },
};

const severityView: Record<FindingView["severity"], StatusView> = {
	high: { label: "Alta", tone: "alta" },
	medium: { label: "Media", tone: "media" },
	low: { label: "Baja", tone: "baja" },
};

const sourceLabel: Record<FindingView["source"], string> = {
	llm: "LLM",
	sast: "SAST",
};

/** Estado del PR (open/closed). */
export function stateOf(state: PrView["state"]): StatusView {
	return stateView[state] ?? { label: state, tone: "neutral" };
}

/** Estado de la corrida de revisión. */
export function reviewStatusOf(status: ReviewStatus): StatusView {
	return reviewStatusView[status] ?? { label: status, tone: "neutral" };
}

/** Severidad de un hallazgo (high/medium/low → alta/media/baja). */
export function severityOf(severity: FindingView["severity"]): StatusView {
	return severityView[severity] ?? { label: severity, tone: "neutral" };
}

/** Origen del hallazgo (llm/sast → LLM/SAST). */
export function sourceOf(source: FindingView["source"]): string {
	return sourceLabel[source] ?? source;
}
