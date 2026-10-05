package main

import (
	"fmt"
	"os"

	"github.com/Andres39128/codeowl/backend/internal/store"
)

func main() {
	hash, err := store.HashPassword(os.Args[1])
	if err != nil {
		panic(err)
	}
	fmt.Print(hash)
}
