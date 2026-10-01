# codeowl

Plataforma **self-hosted, single-org** de revisión automática de código con IA: recibe webhooks de GitHub y GitLab, analiza cada Pull Request con linters y agentes LLM en sandbox, y publica resumen, comentarios inline y diagramas Mermaid directamente en el PR. Incluye un dashboard de triage con cola de PRs priorizada por riesgo.

**Estado:** fase de documentación — la implementación arranca en la Fase 0 de la guía.

## Documentación

| Documento | Qué contiene |
|-----------|--------------|
| [`docs/guia_del_proyecto.md`](docs/guia_del_proyecto.md) | Documento fundacional: alcance, stack, arquitectura, reglas de código, diseño visual, fases y seguridad. **Ante divergencia, la guía manda.** |
| [`docs/mapa_arquitectura.yaml`](docs/mapa_arquitectura.yaml) | Mapa estructural: componentes, contratos, dependencias y flujos. Se actualiza en el mismo PR que la estructura que describe. |
| [`docs/manual_completo_de_coderabbit_ai.md`](docs/manual_completo_de_coderabbit_ai.md) | Referencia funcional del producto externo CodeRabbit.ai — describe el producto que inspira el proyecto, no su alcance. |

### Cómo se gobierna un cambio

Toda decisión de implementación se rastrea a la guía: si un cambio la desactualiza, primero se actualiza la guía y después se escribe código. El mapa vive en el mismo PR que los cambios de estructura.
