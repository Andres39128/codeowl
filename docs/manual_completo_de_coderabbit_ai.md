# Manual Completo, Instructivo y Arquitectura de CodeRabbit.ai

> **Nota de alcance:** este manual describe el producto externo CodeRabbit.ai como referencia funcional. El alcance real de este proyecto — incluidas las divergencias deliberadas (solo GitHub/GitLab, 5 linters, sin Issue Planner, sin `request_changes_workflow`, sin SOC 2) — está definido en `docs/guia_del_proyecto.md` §1.1; ante divergencia, la guía manda. Los detalles del producto externo (modelos, analizadores, esquema de configuración) cambian con el producto: lo acá reflejado fue verificado contra la referencia oficial (`docs.coderabbit.ai/reference/configuration`) en octubre de 2026 y no se mantiene sincronizado — verificar antes de citarlo.

---

## 1. Visión General de CodeRabbit.ai

**CodeRabbit** (`coderabbit.ai`) es una plataforma de revisión de código automatizada basada en Inteligencia Artificial y análisis estático determinista. Se integra directamente en los sistemas de control de versiones (VCS) como GitHub, GitLab, Bitbucket y Azure DevOps para actuar como un revisor senior virtual en cada *Pull Request* (PR) o *Merge Request* (MR).

A diferencia de las herramientas tradicionales de IA que solo analizan el diff local de un archivo, CodeRabbit construye una representación del contexto global de todo el repositorio. Esto le permite comprender dependencias cruzadas, detectar errores lógicos profundos, evaluar riesgos de seguridad, generar diagramas de secuencia e interactuar mediante lenguaje natural con los desarrolladores.

---

## 2. Stack Tecnológico y Arquitectura del Sistema

### 2.1. Stack Tecnológico

> **Nota de evidencia:** CodeRabbit no publica la especificación de su arquitectura interna. Esta sección se infiere de material público (blog, changelog, esquema de configuración) y describe tendencias, no hechos verificados. La única fuente oficial citada en este manual es el esquema de configuración (`docs.coderabbit.ai/reference/configuration`).

Aunque CodeRabbit es un producto SaaS comercial, su infraestructura combina tecnologías modernas para garantizar baja latencia y alta precisión:

* **Lenguajes Backend:**
  * **TypeScript / Node.js & Go:** Utilizados en el orquestador principal, procesamiento concurrente de eventos Webhook y comunicación con APIs de VCS.
  * **Python:** Utilizado en las tuberías (*pipelines*) de datos, generación de embeddings vectoriales e integración con modelos de aprendizaje automático.
* **Procesamiento de Código y AST:**
  * **Tree-sitter:** Motor de parseo para generar Árboles de Sintaxis Abstracta (*Abstract Syntax Trees* o AST) en múltiples lenguajes de programación.
* **Modelos de Lenguaje (LLMs) y Orquestación Multi-Agente:**
  * **Sistema Multi-Modelo:** Orquestación híbrida utilizando modelos avanzados (como OpenAI GPT-4o / GPT-4 Turbo y Anthropic Claude 3.5 Sonnet) junto con modelos especializados en código.
  * **Agentes Especializados:** Agente de Revisión, Agente de Verificación, Agente Conversacional, Agente de Pre-Merge y Agente de Finalización.
* **Recuperación de Contexto (RAG):**
  * Bases de datos vectoriales e índices de grafos de código para realizar *Retrieval-Augmented Generation* (RAG) sobre todo el repositorio.
* **Herramientas SAST y Linters Integrados (Sandbox Execution):**
  * Entorno seguro que ejecuta automáticamente más de 50 analizadores estáticos según el lenguaje detectado (ej. ESLint, Biome, Ruff, Pylint, golangci-lint, Clippy, RuboCop, TruffleHog, Trivy).
* **Seguridad y Cumplimiento:**
  * Certificación **SOC 2 Type II**, cumplimiento **GDPR**, y procesamiento efímero en memoria sin entrenamiento de modelos con el código del cliente.

### 2.2. Flujo de Trabajo Arquitectónico

```
[ Developer Git Push / PR Created ]
               │
               ▼
   [ VCS Webhook Event Handling ]
               │
               ▼
[ Code Cloned in Sandboxed Environment ]
               │
               ├─────────────────────────────────────────┐
               ▼                                         ▼
   [ AST Engine (Tree-sitter) ]           [ 50+ Static Analyzers / SAST ]
               │                                         │
               └────────────────────┬────────────────────┘
                                    ▼
                     [ Contextual RAG Graph Builder ]
                                    │
                                    ▼
                  [ Multi-Agent AI Orchestrator Engine ]
                  (Review, Verification & Logic Agents)
                                    │
                                    ▼
                     [ False-Positive Filtering ]
                                    │
                                    ▼
       [ Integration API: Inline Comments, Mermaid Diagrams & Summaries ]
```

---

## 3. Funcionalidades Principales

1. **Revisión Automatizada Línea por Línea:** Comentarios precisos en el diff del PR sobre errores de lógica, fallos de seguridad (OWASP), rendimiento y legibilidad.
2. **Resumen del PR y Walkthrough:** Explicación ejecutiva en lenguaje natural del propósito del cambio y los componentes afectados.
3. **Diagramas Mermaid Automáticos:** Generación de diagramas de secuencia (*Sequence Diagrams*) para explicar flujos de ejecución modificados.
4. **Sugerencias con Ejecución de 1 Clic (*One-Click Commits*):** Propuestas de código directamente aplicables desde la interfaz del PR.
5. **Agente Conversacional:** Capacidad de responder a comentarios del bot (`@coderabbitai`) solicitando pruebas unitarias, refactorizaciones o explicaciones.
6. **Módulo de Triage (Gestión de Cola de PRs):** Sistema de priorización y análisis de riesgo cross-repositorio para indicar a los revisores humanos qué PR atender primero.
7. **Issue Planner / CodeRabbit Plan:** Módulo que se conecta con Jira, Linear o GitHub Issues para estructurar un plan de codificación antes de escribir la primera línea de código.
8. **Configuración por Código (`.coderabbit.yaml`):** Control de reglas, nivel de rigor, paths excluidos y guías de estilo específicas del proyecto.

---

## 4. Instructivo de Uso e Integración

### Paso 1: Instalación
1. Ingrese a `https://coderabbit.ai` e inicie sesión con su proveedor de Git (GitHub, GitLab, Bitbucket o Azure DevOps).
2. Autorice la aplicación de CodeRabbit en su organización o cuenta personal.
3. Seleccione los repositorios que desea monitorear.

### Paso 2: Configuración del Archivo `.coderabbit.yaml`
Cree un archivo `.coderabbit.yaml` en la raíz de su repositorio para personalizar la conducta de la herramienta.

```yaml
# Archivo de configuración de CodeRabbit
language: "es" # Idioma de los comentarios (ej. es, en)
reviews:
  profile: "chill" # Opciones: "quiet", "chill", "assertive"
  request_changes_workflow: false
  high_level_summary: true
  sequence_diagrams: true
  auto_title_placeholder: "@coderabbitai summary"
  path_filters:
    - "!dist/**"
    - "!vendor/**"
    - "!**/*.min.js"
    - "!node_modules/**"
  path_instructions:
    - path: "**"
      instructions: |
        - Sigue los principios de Código Limpio y SOLID.
        - Prioriza la seguridad en endpoints HTTP.
    - path: "**/*.ts"
      instructions: |
        - Asegura que todas las funciones exportadas tengan tipos explícitos en TypeScript.
chat:
  auto_reply: true
```

### Paso 3: Flujo de Trabajo Diario
1. **Crear PR:** Abra una solicitud de extracción en su plataforma Git.
2. **Revisión Inicial:** CodeRabbit procesará el PR y publicará un resumen general con el diagrama Mermaid correspondiente.
3. **Revisión de Comentarios:** Revise las sugerencias en las líneas de código. Para aceptar un cambio, haga clic en *Apply Suggestion*.
4. **Interactuar con el Bot:** Puede responder directamente a cualquier comentario de CodeRabbit usando el comando `@coderabbitai`:
   * `@coderabbitai genera pruebas unitarias con Jest para este método.`
   * `@coderabbitai ¿cuál es la complejidad temporal en notación $O(n)$ de este algoritmo?`
   * `@coderabbitai refactoriza este bloque para usar async/await.`

---

## 5. Historias de Usuario (User Stories)

### Historia de Usuario 1: Revisión Automática de Seguridad y Lógica en Pull Requests
* **Como:** Desarrollador Backend.
* **Quiero:** Que CodeRabbit revise automáticamente mis Pull Requests apenas sean creados.
* **Para:** Identificar vulnerabilidades de seguridad y errores lógicos antes de solicitar la revisión de un compañero de equipo.

#### Criterios de Aceptación:
1. Al abrir o actualizar un PR, CodeRabbit debe responder en menos de 3 minutos con un resumen de los cambios.
2. Debe señalar errores de lógica o riesgos de seguridad con comentarios en la línea exacta del código (*inline*).
3. Debe incluir un bloque de sugerencia de código listo para ser aplicado con un solo clic.

---

### Historia de Usuario 2: Generación Automática de Pruebas Unitarias mediante Chat
* **Como:** Desarrollador Frontend.
* **Quiero:** Interactuar con CodeRabbit mediante menciones (`@coderabbitai`) en los comentarios de un PR.
* **Para:** Solicitar la generación automática de suites de prueba sin salir de GitHub/GitLab.

#### Criterios de Aceptación:
1. Cuando el usuario comente `@coderabbitai crea pruebas unitarias para esta función`, el bot debe responder con el código correspondiente usando el framework de pruebas detectado en el proyecto (ej. Vitest/Jest).
2. El código de prueba propuesto debe incluir casos de éxito y manejo de bordes (*edge cases*).

---

### Historia de Usuario 3: Priorización de Code Reviews en el Equipo con Triage
* **Como:** Engineering Manager / Tech Lead.
* **Quiero:** Visualizar una cola centralizada de PRs ordenados por nivel de riesgo e impacto con la función Triage.
* **Para:** Asignar los recursos del equipo a las revisiones más críticas y evitar cuellos de botella en la entrega.

#### Criterios de Aceptación:
1. El panel de Triage debe clasificar los PRs abiertos por etiquetas de riesgo (ej. Alto, Medio, Bajo).
2. Debe indicar qué PRs requieren atención humana urgente y cuáles pueden ser aprobados rápidamente tras la verificación de la IA.

---

### Historia de Usuario 4: Estandarización de Normas del Proyecto mediante `.coderabbit.yaml`
* **Como:** DevOps / Líder de Arquitectura.
* **Quiero:** Definir reglas personalizadas en un archivo `.coderabbit.yaml` en la raíz del repositorio.
* **Para:** Garantizar que todas las revisiones de IA apliquen los estándares de arquitectura y convenciones de nombres propios de la empresa.

#### Criterios de Aceptación:
1. El bot debe leer las directivas especificadas bajo el campo `instructions` en el YAML.
2. Los comentarios generados por la IA deben reflejar estrictamente las reglas definidas (ej. exigir tipos explícitos en TypeScript o el uso de patrones DTO).
3. Debe ignorar los archivos que coincidan con los patrones declarados en `path_filters`.

---

## 6. Resumen de Beneficios y Métricas de Impacto

> Cifras según material de marketing del proveedor; no verificables contra documentación técnica.

* **Precisión de Sugerencias:** Alto nivel de adopción directa por parte de los ingenieros gracias al filtrado de falsos positivos y verificación estática.
* **Reducción del Tiempo de Cycle Time:** Disminución de hasta un 50% en el tiempo promedio de aprobación de Pull Requests.
* **Calidad de Código y Seguridad:** Reducción sustancial de regresiones en producción al actuar como una puerta de enlace de calidad previa al Merge.
