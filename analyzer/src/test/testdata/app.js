// Fixture JS: función + clase + método + imports (§6 F4).
import { hola } from './saludos.js';
const path = require('node:path');

function chau(nombre) {
  return `chau ${nombre}`;
}

class Greeter {
  greet() {
    return hola(this.nombre);
  }
}
