# Guía para no-programadores y creador de prompts de corrección

Un usuario sin conocimientos de programación usa codeowl para revisar sus
PRs: necesita saber qué hacer en cada paso, cómo interpretar y verificar una
alerta (hallazgo), y cómo corregirla. El pedido incluye un "creador de
prompts": por cada hallazgo, armar un prompt completo con contexto para
pegar en su asistente de IA (ChatGPT, Claude, etc.) y que este lo corrija o
explique.

## Specs

- **S1 — Creador de prompts por hallazgo**: en cada hallazgo del detalle de
  PR, un botón "Arreglar con IA" abre un modal con dos prompts armados y
  copiables: "Corregir" (rol + contexto + hallazgo + sugerencia + snippet
  del diff del archivo + qué pedir: el fix y una explicación en lenguaje
  simple) y "Explicar" (qué significa la alerta y por qué importa, sin
  código). Copiar con un clic. Sin envío a servicios externos: el usuario
  pega el prompt donde quiera.
- **S2 — Guía paso a paso en el dashboard**: página "Guía" (#/guia) visible
  en la navegación: (1) conectar el repo, (2) abrir la salud del repo y
  qué hacer si un check falla, (3) revisar los hallazgos (severidad,
  origen SAST/LLM, verificación), (4) corregir: la sugerencia aplicable en
  GitHub o el prompt de S1, (5) verificar que quedó corregido (nueva
  corrida, aplicar "No aplica" a lo que no corresponde). En lenguaje
  llano, sin jerga sin explicar, con links a las pantallas.
- **S3 — Doc espejo**: docs/guia_usuario.md con el mismo contenido (fuente
  para el que prefiere leer fuera del dashboard) y la guía del proyecto
  referencia su existencia.

## Tasks

- T1 (S1): componente PromptModal + botón en HallazgoItem + tests. [db9f90da]
- T2 (S2): página Guía + nav + tests. [8ab8c957]
- T3 (S3): docs/guia_usuario.md + referencia en la guía del proyecto. [022c6c31]

## Log

- L1 (pedido original, verbatim): "Has de cuenta que una persona con pocos
  conocimientos en programación desea usar la herramienta, necesitamos
  crear una guía clara de paso a paso, que hacer en cada punto, como
  verificar y corregir las alertas y un creador de prompts para realizar
  las correcciones de las alertas y todo lo que se descubra"
- L4: verificado en navegador real: Guía (#/guia) con los 5 pasos y links
  correctos, PromptModal con tabs Corregir/Explicar, archivo y línea del
  hallazgo real en el prompt, pedidos completos; hunk con fallback honesto
  cuando el archivo no está en el diff. Dashboard 121/121.
- L5: la review del PR #16 destapó que el test de rotación (BD compartida)
  había dejado los api_key reales irrecuperables (doble cifrado con claves
  de test). Fix de raíz (BD scratch, 8250b8f4), recuperación de los dos
  proveedores y api_key placeholder 'local' para Qwen @ localhost:8081 (el
  servidor no valida la clave). MiniMax requiere reingreso de la clave
  real por el usuario + red que no corte TLS hacia api.minimax.io.
