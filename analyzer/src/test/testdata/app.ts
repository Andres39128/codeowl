// Fixture TS: el CLI lo parsea con el grammar de JavaScript (limitación
// documentada en symbols.go — sintaxis exclusiva de TS ⇒ 0 símbolos).
import { _hola } from './saludos.js'; // eslint-disable-line @typescript-eslint/no-unused-vars, no-unused-vars

export function chau(nombre) {
  return `chau ${nombre}`;
}
