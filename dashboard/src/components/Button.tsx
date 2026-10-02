/** Botón base (guía §5.3): variantes primary/secondary/ghost sobre tokens. */

import type { ButtonHTMLAttributes } from "preact";

const variantClasses = {
	primary: "bg-action-primary text-action-primary-text hover:opacity-90",
	secondary: "border border-border-subtle text-text-primary hover:opacity-80",
	ghost: "text-text-muted hover:text-text-primary",
} as const;

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
	variant?: keyof typeof variantClasses;
}

export function Button({
	variant = "primary",
	class: className,
	...rest
}: ButtonProps) {
	const classes =
		"inline-flex items-center justify-center rounded-md px-3 py-1.5 text-sm font-medium disabled:opacity-50 disabled:pointer-events-none";
	return (
		<button
			class={`${classes} ${variantClasses[variant]} ${className ?? ""}`}
			{...rest}
		/>
	);
}
