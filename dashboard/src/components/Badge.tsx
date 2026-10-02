/** Badge de severidad/estado (guía §5.3). El ícono y el texto los aporta el
 * consumidor: el estado nunca se comunica solo con color (guía §5.2.3). */

import type { HTMLAttributes } from "preact";

const toneClasses = {
	alta: "text-severity-alta",
	media: "text-severity-media",
	baja: "text-severity-baja",
	neutral: "text-text-muted",
} as const;

export type BadgeTone = keyof typeof toneClasses;

interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
	tone?: BadgeTone;
}

export function Badge({
	tone = "neutral",
	class: className,
	children,
	...rest
}: BadgeProps) {
	return (
		<span
			class={`inline-flex items-center gap-1 rounded-full border border-border-subtle px-2 py-0.5 text-xs ${toneClasses[tone]} ${className ?? ""}`}
			{...rest}
		>
			{children}
		</span>
	);
}
