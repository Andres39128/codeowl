# Resumen de Funciones y Capacidades de CodeRabbit

**CodeRabbit** es una plataforma de revisión de código automatizada impulsada por Inteligencia Artificial que se instala como aplicación de Git en plataformas como **GitHub, GitLab, Bitbucket y Azure DevOps**, además de integrarse en entornos de desarrollo (IDEs como VS Code, Cursor o Windsurf) y en la interfaz de línea de comandos (CLI).

Su propósito principal es actuar como un **revisor o "senior engineer" automatizado** que analiza los *Pull Requests* (PR) en tiempo real. Ayuda a los equipos de desarrollo a detectar fallos de lógica, vulnerabilidades de seguridad y errores sintácticos antes de fusionar el código, reduciendo el tiempo de revisión manual hasta en un 35% - 42%.

---

## Funciones Principales y qué hace cada una

* **AI Code Reviews (Revisiones de Código)**: Examina el diff de código en cada PR y genera comentarios en línea señalando errores, posibles fallos en tiempo de ejecución o problemas de calidad, combinando modelos de IA con más de 40 linters y herramientas estáticas (SAST).
* **PR Summaries & Walkthroughs (Resúmenes de PR)**: Crea automáticamente una descripción ejecutiva en lenguaje natural que explica los cambios realizados, qué archivos se modificaron, su impacto funcional y borradores de notas de lanzamiento (*release notes*).
* **One-Click Fixes (Autofix)**: Proporciona sugerencias de código listas para aplicar directamente en el PR con un solo clic, evitando la copia manual de fragmentos.
* **Change Stack (Pila de Cambios)**: Reorganiza PRs complejos y extensos en capas lógicas o "cohortes" estructurados según sus relaciones de dependencia.
* **Sequence Diagrams (Diagramas de Secuencia)**: Genera automáticamente diagramas interactivos en sintaxis *Mermaid* dentro de la descripción del PR para visualizar el flujo de ejecución entre componentes o llamadas a API.
* **CodeRabbit Triage**: Prioriza y organiza la cola de PRs pendientes entre repositorios según su nivel de riesgo, esfuerzo e impacto, guiando al equipo sobre qué revisar primero.
* **CodeRabbit Secure (Análisis de Seguridad)**: Escanea el código en busca de claves o secretos expuestos (*secrets*), inyecciones SQL, vulnerabilidades del OWASP Top 10 y analiza el radio de impacto (*Blast Radius*) de los cambios.
* **Interactive Chat / Agent**: Permite a los desarrolladores interactuar en la conversación del PR o en canales como Slack/Discord mencionando a `@coderabbitai` para hacer preguntas sobre la arquitectura, pedir refactorizaciones o solicitar aclaraciones.
* **Finishing Touches (Toques Finales)**: Ofrece herramientas de automatización para generar suites de pruebas unitarias (*Unit Tests*), crear o mejorar documentación (*docstrings*), simplificar código y resolver conflictos de fusión (*merge conflicts*).
* **Learnings & Knowledge Base (Aprendizaje Adaptativo)**: Almacena las directrices del proyecto (como archivos `.coderabbit.yaml`, `.cursorrules`, etc.) y aprende de las respuestas o rechazos del equipo para adaptar sus sugerencias con el tiempo y reducir falsos positivos.
* **Slop Detection**: Identifica y marca contribuciones de baja calidad o código de relleno generado automáticamente por herramientas de IA.

---

## Lo más destacado y lo más usado

### Lo más destacado (Diferenciadores clave)

* **Indexación AST (Abstract Syntax Tree)**: A diferencia de los escáneres convencionales que leen el texto del diff aislado, CodeRabbit construye un árbol de sintaxis abstracta y un grafo de llamadas a nivel de todo el repositorio para entender el contexto global y las dependencias entre archivos.
* **Garantía de Privacidad y Retención Cero (ZDR)**: Los datos se procesan en contenedores efímeros en memoria y **nunca** se utilizan para entrenar modelos externos ni públicos.
* **Acceso Gratuito para Código Abierto (OSS)**: Es **100% gratuito e ilimitado** para repositorios públicos, incluyendo funciones avanzadas de nivel de equipo.

### Lo más usado

* **Los Resúmenes Automáticos de PR**: Es la función más valorada en el día a día por los ingenieros para comprender rápidamente el propósito de un cambio sin tener que descifrar línea por línea.
* **Los Comentarios en Línea con Sugerencias y Chat**: La detección rápida de errores puntuales y la posibilidad de chatear con el bot en el propio diff para ajustar el código.

---

## Tipo de visualización y ordenamiento

CodeRabbit transforma la vista plana tradicional de un gestor de Git en un formato estructurado y navegable:

### 1. Orden por Capas de Dependencia (Cohortes)

En lugar de mostrar los archivos modificados en orden alfabético estricto (como hace GitHub por defecto), CodeRabbit los agrupa y los muestra en el **orden lógico conceptual de la modificación** (por ejemplo: esquema de base de datos → lógica de negocio → API → interfaz de usuario → pruebas).

### 2. Visualización Gráfica e Interactiva

* **Diagramas de Secuencia**: Renderiza gráficos interactivos Mermaid en el comentario inicial para mostrar cómo fluyen los datos a través de los microservicios o módulos.
* **Secciones Desplegables (*Collapsible Walkthrough*)**: Agrupa los detalles en pestañas o secciones contraíbles para mantener el comentario principal limpio.

### 3. Clasificación con Insignias y Niveles de Gravedad

Cada comentario incluye etiquetas visuales claras para ayudar a filtrar la urgencia del hallazgo:

* **Categorías (Emoji Badges)**: 🔒 *Security & Privacy*, 🩺 *Stability & Availability*, 🗄 *Data Integrity*, 🎯 *Functional Correctness*, 🚀 *Performance*, 📐 *Maintainability*.
* **Gravedad (Severity Levels)**: 🔴 **Critical** (errores severos), 🟠 **Major** (problemas relevantes), 🟡 **Minor** (ajustes recomendados), 🔵 **Trivial** (sugerencias leves) y ⚪ **Info** (comentarios informativos).
