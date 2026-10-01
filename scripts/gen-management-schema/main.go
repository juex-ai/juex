package main

import (
	"log"
	"os"

	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
)

func main() {
	data, err := managementhttp.GenerateTypeScript()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("frontend/src/management/schema.ts", data, 0o644); err != nil {
		log.Fatal(err)
	}
}
