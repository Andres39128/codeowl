/** Banner de contexto (guía §5.3): mensajes de guarda y datos de una sola
 * vista (contraseña temporal, §3.4). El estado nunca se comunica solo con
 * color: warning/error toman role="alert" y el texto acompaña (§5.2.3). */

import type { HTMLAttributes } from "preact";

const toneClasses = {
	info: "border-accent text-text-primary",
	success: "border-severity-baja text-severity-baja",
	warning: "border-severity-media text-severity-media",
	error: "border-severity-alta text-severity-alta",
} as const;

export type BannerTone = keyof typeof toneClasses;

interface BannerProps extends HTMLAttributes<HTMLDivElement> {
	tone?: BannerTone;
}

export function Banner({
	tone = "info",
	class: className,
	children,
	...rest
}: BannerProps) {
	const role = tone === "warning" || tone === "error" ? "alert" : "status";
	return (
		<div
			role={role}
			class={`rounded-md border-l-4 bg-bg-surface p-3 text-sm ${toneClasses[tone]} ${className ?? ""}`}
			{...rest}
		>
			{children}
		</div>
	);
}
