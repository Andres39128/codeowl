# Guía de uso para no-programadores

Esta guía es el espejo fuera del dashboard de la página **Guía** (`#/guia`):
cómo usar codeowl sin saber programar, de conectar tu repositorio a corregir
las alertas con ayuda de la IA. La página del dashboard es la versión
navegable con links directos a las pantallas.

## 1. Conectá tu repositorio

En **Configuración → Repos** (solo administradores) tocá **Conectar repo**.
Elegí GitHub, escribí el dueño y el nombre del repositorio (los ves en la
dirección: `github.com/dueño/nombre`) y tocá **Detectar ID con la App**: el
número que falta se completa solo. Si te dice que falta instalar la App, el
mensaje te explica cómo.

## 2. Revisá la salud del repo

Cada repo en la lista muestra su **salud**: tres características que deben
estar en verde para que las revisiones funcionen. Si algo está en rojo, al
lado dice cómo arreglarlo:

- **App instalada**: la llave que le permite a la herramienta leer tu código
  y comentar en tus pull requests.
- **Webhook activo**: el aviso automático de GitHub cuando subís cambios. Si
  nunca llegó uno, la herramienta necesita una dirección pública (servidor o
  túnel).
- **Proveedor review activo**: el "cerebro" (modelo de IA) que lee el código.
  Se configura una vez en **Configuración → Proveedores**.

El botón **Revisar ahora** dispara una revisión del último pull request
abierto sin esperar el aviso automático.

## 3. Leé las alertas (hallazgos)

Abrí tu PR en **PRs**: verás el resumen, el cambio completo (el "diff") y la
lista de hallazgos — cada problema encontrado, del más grave al menos.

- **Severidad**: Alta = puede romper algo importante. Media = conviene
  arreglarlo. Baja = detalle menor.
- **Origen**: SAST = lo encontró un análisis automático de reglas fijas (casi
  siempre certero). LLM = lo encontró la IA al leer tus cambios.
- **Verificación**: "Verificado" = otra pasada de la IA lo confirmó. "Falso
  positivo" = se descartó y no se publicó. "Sin verificar" = no se pudo
  confirmar.

La vista **Agrupar por archivo** ordena los hallazgos por archivo afectado; los
filtros (severidad, origen, búsqueda) focalizan en lo que importa.

## 4. Corregí con la IA (Arreglar con IA)

Cada hallazgo tiene el botón **Arreglar con IA**: arma un mensaje completo
(problema, archivo, cambio y sugerencia incluidos) listo para pegar en el
asistente de IA que uses — ChatGPT, Claude, Gemini o cualquiera. Dos
versiones:

- **Corregir**: pide el arreglo listo para copiar, una explicación en
  lenguaje simple y los pasos exactos para aplicarlo en GitHub.
- **Explicar**: pide qué significa la alerta, por qué importa y cuánto
  preocuparte, sin código.

Algunos hallazgos traen una **Sugerencia**: código corregido que GitHub
aplica con un clic desde el comentario del PR (botón *Ver en GitHub*).

## 5. Verificá que quedó arreglado

Después de aplicar el cambio y subirlo, dispará la revisión otra vez
(**Revisar ahora** o el aviso automático). La nueva corrida te lo dice sin
que compares a ojo:

- **Resueltos en esta corrida (N)**: sección colapsada al final de los
  hallazgos con lo que ya no aparece — arreglos confirmados.
- **↻ Persistente**: etiqueta en el hallazgo que reapareció — la misma
  alerta de la corrida anterior sigue sin arreglarse. Priorizalo.
- Si la sugerencia muestra "✔ Aplicada", la herramienta detectó tu arreglo.

La página **Triage** suma la **tasa de resolución** (qué proporción de los
hallazgos desaparece entre corridas) con el detalle de resueltos vs
persistentes por repositorio.

Si un hallazgo no corresponde, tocá **No aplica**: se guarda para auditoría,
se atenúa y no vuelve a molestar en futuras revisiones del repositorio (y no
cuenta como resuelto ni como persistente).

## Si algo no funciona

El panel **Cola de Jobs** muestra si una revisión está pendiente, en curso o
terminó con error, con el mensaje exacto del fallo; la pestaña **Historial**
lista las últimas corridas con su duración y resultado.
