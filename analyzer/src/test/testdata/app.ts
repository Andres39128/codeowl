// Fixture TS: el CLI lo parsea con el grammar de JavaScript (limitación
// documentada en symbols.go — sintaxis exclusiva de TS ⇒ 0 símbolos).
import { hola } from './saludos.js';

export function chau(nombre) {
  return `chau ${nombre}`;
}
