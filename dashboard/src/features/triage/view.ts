/** Banda de riesgo del score (guía §6 F5, decisiones 6 y 9): el score 0-100
 * se comunica en tres bandas con los mismos tonos que las severidades
 * (§5.2.3 — nunca solo color, el texto acompaña). null = el PR aún no tiene
 * corrida con score → la celda muestra "—" en neutral, no una banda falsa. */

import type { BadgeTone } from "../../components/Badge";

interface RiskBand {
	label: string;
	tone: BadgeTone;
}

/** Bandas: 0-39 baja, 40-69 media, 70-100 alta. Un score fuera de rango cae
 * en la banda contigua (defensivo, mismo criterio que los mapas de prs/view). */
export function riskBandOf(score: number | null): RiskBand | null {
	if (score === null) return null;
	if (score < 40) return { label: "Baja", tone: "baja" };
	if (score < 70) return { label: "Media", tone: "media" };
	return { label: "Alta", tone: "alta" };
}
