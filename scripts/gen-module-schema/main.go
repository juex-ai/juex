package main

import (
	"github.com/juex-ai/juex/internal/entrypoints/agenthttp"
	"log"
	"os"
)

func main() {
	data, err := web.GenerateModuleTypeScript()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile("frontend/src/module-schema.ts", data, 0o644); err != nil {
		log.Fatal(err)
	}
}
