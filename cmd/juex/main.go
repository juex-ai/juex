// Juex CLI entry point. All real work lives in github.com/juex-ai/juex/internal/cli.
package main

import (
	"os"

	"github.com/juex-ai/juex/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
