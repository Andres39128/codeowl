# Fixture repo del analyzer (mapa analyzer.cli.pruebas)

Repo mínimo con símbolos e imports conocidos en los 4 lenguajes del stack
(§6 F4). Lo consumen `symbols_test.go` y el smoke manual del CLI:

    cd analyzer/src && go build -o /tmp/analyzer . && /tmp/analyzer --symbols test

- `testdata/main.go` — go: type + interface + función + método.
- `testdata/app.js` — js: función + clase + método; import + require.
- `testdata/app.py` — python: función + clase + método; import + from-import.
- `testdata/lib.rs` — rust: fn + struct + trait + impl; use.
- `testdata/app.ts` — .ts parseado con el grammar de JavaScript (limitación).
- `testdata/broken.go` — error de sintaxis deliberado: cero símbolos, el run sigue.
- `README.md` — no es código: el CLI no lo analiza.

Los fixtures .go viven en testdata/ porque la toolchain de Go ignora ese
directorio: broken.go jamás rompe `go test ./...`.
