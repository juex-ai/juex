// Juex CLI entry point. All real work lives in github.com/juex-ai/juex/internal/cli.
package main

import (
	"os"

	"github.com/juex-ai/juex/internal/cli"

	// Blank import installs DNS + TLS root fallbacks at startup so the
	// binary works on environments that lack /etc/resolv.conf or a
	// system CA bundle (notably Termux on Android). No-op on standard
	// Linux/macOS/Windows.
	_ "github.com/juex-ai/juex/internal/netbootstrap"
)

func main() {
	os.Exit(cli.Execute())
}
