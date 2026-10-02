/** Etiqueta + control de formulario, reutilizable por los modals de settings.
 * El estilo de control va aparte para selects e inputs por igual (guía §5.1). */

import type { ComponentChildren } from "preact";

/** Estilo compartido de inputs y selects sobre tokens. */
export const inputClass =
	"mt-1 w-full rounded-md border border-border-subtle bg-bg-elevated px-3 py-1.5 text-sm";

interface FieldProps {
	label: string;
	htmlFor: string;
	children: ComponentChildren;
}

export function Field({ label, htmlFor, children }: FieldProps) {
	return (
		<div class="mb-3">
			<label class="block text-sm" for={htmlFor}>
				{label}
			</label>
			{children}
		</div>
	);
}
