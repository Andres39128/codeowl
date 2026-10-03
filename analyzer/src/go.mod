module github.com/Andres39128/codeowl/analyzer/src

go 1.27

// Pines tree-sitter (§9.4: reproducibilidad — bump = PR). Verificado contra
// proxy.golang.org: los tags v0.25.x de los grammars se generaron con
// tree-sitter CLI 0.25 (ABI 15) y el runtime Go solo soporta ABI 13-14 —
// parser.SetLanguage falla. Los pares probados en runtime (parseo real de los
// 4 lenguajes) son estos; bump de grammars = esperar runtime Go con ABI 15.
require (
	github.com/tree-sitter/go-tree-sitter v0.24.0 // runtime (última release)
	github.com/tree-sitter/tree-sitter-go v0.23.4
	github.com/tree-sitter/tree-sitter-javascript v0.23.1
	github.com/tree-sitter/tree-sitter-python v0.23.6
	github.com/tree-sitter/tree-sitter-rust v0.23.3
)

require github.com/mattn/go-pointer v0.0.1 // indirect
