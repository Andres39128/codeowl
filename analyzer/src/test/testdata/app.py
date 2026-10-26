"""Fixture Python: función + clase + método + imports (§6 F4)."""
import os  # noqa: F401
from collections import OrderedDict  # noqa: F401


def hola(nombre):
    return f"hola {nombre}"


class Greeter:
    def greet(self):
        return hola(self.nombre)  # type: ignore[attr-defined]
