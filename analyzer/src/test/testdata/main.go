package main

import (
	"fmt"
	alias "os/exec"
)

// Greeter saluda (fixture de símbolos — ver symbols_test.go).
type Greeter struct {
	Name string
}

// Speaker es la interfaz mínima del fixture.
type Speaker interface {
	Speak() string
}

// Greet devuelve el saludo de un Greeter.
func Greet(g Greeter) string {
	return fmt.Sprintf("hola %s", g.Name)
}

// Speak implementa Speaker.
func (g Greeter) Speak() string {
	return g.Name
}
