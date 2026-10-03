"""Fixture Python: función + clase + método + imports (§6 F4)."""
import os
from collections import OrderedDict


def hola(nombre):
    return f"hola {nombre}"


class Greeter:
    def greet(self):
        return hola(self.nombre)
