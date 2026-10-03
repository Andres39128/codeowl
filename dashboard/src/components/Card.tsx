/** Card contenedora (guía §5.3): superficie con título y slot de acciones
 * opcionales. Solo roles de tokens (§5.1), nunca valores crudos. */

import type { ComponentChildren, HTMLAttributes } from "preact";

interface CardProps extends HTMLAttributes<HTMLElement> {
	title?: string;
	actions?: ComponentChildren;
}

export function Card({
	title,
	actions,
	class: className,
	children,
	...rest
}: CardProps) {
	return (
		<section
			class={`rounded-lg border border-border-subtle bg-bg-surface p-4 ${className ?? ""}`}
			{...rest}
		>
			{(title !== undefined || actions !== undefined) && (
				<div class="mb-3 flex items-center justify-between gap-2">
					{title !== undefined && (
						<h2 class="text-base font-semibold">{title}</h2>
					)}
					{actions !== undefined && (
						<div class="flex items-center gap-2">{actions}</div>
					)}
				</div>
			)}
			{children}
		</section>
	);
}
