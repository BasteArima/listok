// Пересобирает shortcut/listok.plist из описания команды: go run ./shortcut/generate
package main

import (
	"log"
	"os"

	"github.com/BasteArima/listok/shortcut"
)

func main() {
	if err := os.WriteFile("shortcut/listok.plist", shortcut.Plist(), 0o644); err != nil {
		log.Fatal(err)
	}
}
