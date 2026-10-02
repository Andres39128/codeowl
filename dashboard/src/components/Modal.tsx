/** Modal base (guía §5.3): cierra con ESC o click en el fondo; el contenido no
 * propaga el click. Sin dependencias: overlay div + listener de teclado. */

import type { ComponentChildren } from "preact";
import { useEffect } from "preact/hooks";
import { Button } from "./Button";

interface ModalProps {
	title: string;
	onClose: () => void;
	children: ComponentChildren;
}

export function Modal({ title, onClose, children }: ModalProps) {
	useEffect(() => {
		const onKeyDown = (event: KeyboardEvent) => {
			if (event.key === "Escape") onClose();
		};
		document.addEventListener("keydown", onKeyDown);
		return () => document.removeEventListener("keydown", onKeyDown);
	}, [onClose]);

	return (
		// El fondo recibe click para cerrar; el ESC ya está cubierto por el
		// listener de document de arriba (reglas de a11y conformes por diseño).
		// biome-ignore lint/a11y/noStaticElementInteractions: overlay, no control semántico
		// biome-ignore lint/a11y/useKeyWithClickEvents: ESC ya cierra (listener en document)
		<div
			data-testid="modal-backdrop"
			class="fixed inset-0 z-50 flex items-center justify-center bg-bg-scrim p-4"
			onClick={onClose}
		>
			{/* biome-ignore lint/a11y/useKeyWithClickEvents: ESC ya cierra (listener en document) */}
			<div
				role="dialog"
				aria-modal="true"
				aria-label={title}
				class="w-full max-w-md rounded-lg border border-border-subtle bg-bg-elevated p-4"
				onClick={(event) => event.stopPropagation()}
			>
				<div class="mb-3 flex items-center justify-between">
					<h2 class="text-base font-semibold">{title}</h2>
					<Button variant="ghost" onClick={onClose} aria-label="Cerrar">
						✕
					</Button>
				</div>
				{children}
			</div>
		</div>
	);
}
