// Fixture Rust: fn + struct + trait + impl + imports (§6 F4).
use std::collections::HashMap;
use crate::saludos::hola;

pub struct Greeter {
    pub nombre: String,
    pub cache: HashMap<String, String>,
}

pub trait Speaker {
    fn speak(&self) -> String;
}

impl Greeter {
    pub fn speak(&self) -> String {
        self.nombre.clone()
    }
}

pub fn hola(nombre: &str) -> String {
    format!("hola {nombre}")
}
