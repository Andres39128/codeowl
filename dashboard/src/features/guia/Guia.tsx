/** Guía paso a paso (S2) para quien no programa: conectar un repo, entender
 * la salud, leer los hallazgos y corregirlos — con el creador de prompts
 * como camino principal de corrección. Pública: member también la ve. */

const pasos: {
	titulo: string;
	cuerpo: preact.ComponentChildren;
}[] = [
	{
		titulo: "1. Conectá tu repositorio",
		cuerpo: (
			<>
				<p>
					Andá a{" "}
					<a class="text-action-primary underline" href="#/settings/repos">
						Configuración → Repos
					</a>{" "}
					(solo administradores) y tocá <strong>Conectar repo</strong>. Elegí
					GitHub, escribí el dueño y el nombre del repositorio (los ves en la
					dirección de GitHub: <code>github.com/dueño/nombre</code>) y tocá{" "}
					<strong>Detectar ID con la App</strong>: el número que falta se
					completa solo. Si te dice que falta instalar la App, el botón te
					explica cómo.
				</p>
			</>
		),
	},
	{
		titulo: "2. Revisá la salud del repo",
		cuerpo: (
			<>
				<p>
					En la misma pantalla, cada repo muestra su <strong>salud</strong>:
					tres características que deben estar en verde para que las revisiones
					funcionen. Si algo está en rojo, al lado te dice exactamente cómo
					arreglarlo:
				</p>
				<ul class="mt-2 list-disc pl-5">
					<li>
						<strong>App instalada</strong>: la llave que le permite a la
						herramienta leer tu código y comentar en tus pull requests.
					</li>
					<li>
						<strong>Webhook activo</strong>: el aviso automático de GitHub
						cuando subís cambios. Si nunca llegó uno, la herramienta necesita
						una dirección pública (un servidor o un túnel).
					</li>
					<li>
						<strong>Proveedor review activo</strong>: el "cerebro" (modelo de
						IA) que lee el código. Se configura una vez en{" "}
						<a
							class="text-action-primary underline"
							href="#/settings/providers"
						>
							Configuración → Proveedores
						</a>
						.
					</li>
				</ul>
				<p class="mt-2">
					El botón <strong>Revisar ahora</strong> dispara una revisión del
					último pull request abierto sin esperar el aviso automático: usalo
					cuando quieras.
				</p>
			</>
		),
	},
	{
		titulo: "3. Leé las alertas (hallazgos)",
		cuerpo: (
			<>
				<p>
					Entrá a{" "}
					<a class="text-action-primary underline" href="#/prs">
						PRs
					</a>{" "}
					y abrí el tuyo. Vas a ver el resumen, el cambio completo (el "diff") y
					la lista de hallazgos: cada problema encontrado, ordenado del más
					grave al menos.
				</p>
				<ul class="mt-2 list-disc pl-5">
					<li>
						<strong>Severidad</strong>: Alta = puede romper algo importante.
						Media = conviene arreglarlo. Baja = detalle menor.
					</li>
					<li>
						<strong>Origen</strong>: SAST = lo encontró un análisis automático
						del código (reglas fijas, casi siempre certero). LLM = lo encontró
						la IA al leer tus cambios.
					</li>
					<li>
						<strong>Verificación</strong>: "Verificado" = otra pasada de la IA
						confirmó que es real. "Falso positivo" = se descartó y no se publicó
						en GitHub. "Sin verificar" = no se pudo confirmar.
					</li>
				</ul>
				<p class="mt-2">
					Con la vista <strong>Agrupar por archivo</strong> los hallazgos se
					ordenan por archivo afectado, y los filtros (severidad, origen,
					búsqueda) te dejan focalizar en lo que importa.
				</p>
			</>
		),
	},
	{
		titulo: "4. Corregí con la IA (Arreglar con IA)",
		cuerpo: (
			<>
				<p>
					Cada hallazgo tiene el botón <strong>Arreglar con IA</strong>: arma
					por vos un mensaje completo (con el problema, el archivo, el cambio y
					la sugerencia) listo para pegar en el asistente de IA que uses —
					ChatGPT, Claude, Gemini o cualquiera. Dos versiones:
				</p>
				<ul class="mt-2 list-disc pl-5">
					<li>
						<strong>Corregir</strong>: le pide el arreglo listo para copiar, una
						explicación en lenguaje simple y los pasos exactos para aplicarlo en
						GitHub.
					</li>
					<li>
						<strong>Explicar</strong>: le pide que te cuente qué significa la
						alerta, por qué importa y cuánto preocuparte, sin código.
					</li>
				</ul>
				<p class="mt-2">
					Algunos hallazgos vienen con una <strong>Sugerencia</strong>: código
					corregido que GitHub puede aplicar con un clic desde el comentario del
					PR (botón <em>Ver en GitHub</em>).
				</p>
			</>
		),
	},
	{
		titulo: "5. Verificá que quedó arreglado",
		cuerpo: (
			<>
				<p>
					Después de aplicar el cambio y subirlo a GitHub, volvé a disparar la
					revisión (<strong>Revisar ahora</strong> en la salud del repo, o el
					aviso automático). Si el hallazgo desapareció de la lista, quedó
					corregido; si aparece "✔ Aplicada" en la sugerencia, la herramienta
					detectó tu arreglo.
				</p>
				<p class="mt-2">
					Si un hallazgo no corresponde o no querés volver a verlo, tocá{" "}
					<strong>No aplica</strong>: se guarda para auditoría, se atenúa y no
					vuelve a molestar en futuras revisiones del mismo repositorio.
				</p>
			</>
		),
	},
];

export function Guia() {
	return (
		<section class="max-w-3xl">
			<h1 class="mb-1 text-xl font-semibold">Guía paso a paso</h1>
			<p class="mb-6 text-sm text-text-muted">
				Cómo usar esta herramienta sin saber programar: de conectar tu
				repositorio a corregir las alertas con ayuda de la IA.
			</p>
			<ol class="flex flex-col gap-4">
				{pasos.map((paso) => (
					<li
						key={paso.titulo}
						class="rounded-md border border-border-subtle p-4"
					>
						<h2 class="mb-2 text-base font-semibold">{paso.titulo}</h2>
						<div class="text-sm leading-relaxed">{paso.cuerpo}</div>
					</li>
				))}
			</ol>
			<p class="mt-6 text-sm text-text-muted">
				¿Algo no funciona? El panel{" "}
				<a class="text-action-primary underline" href="#/queue">
					Cola de Jobs
				</a>{" "}
				muestra si una revisión está pendiente, en curso o terminó con error,
				con el mensaje exacto del fallo.
			</p>
		</section>
	);
}
