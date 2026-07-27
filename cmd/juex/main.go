// Juex CLI entry point. All real work lives in github.com/juex-ai/juex/internal/cli.
package main

import (
	"fmt"
	"os"

	"github.com/juex-ai/juex/internal/cli"
	"github.com/juex-ai/juex/internal/sandbox"

	// Blank import installs DNS + TLS root fallbacks at startup so the
	// binary works on environments that lack /etc/resolv.conf or a
	// system CA bundle (notably Termux on Android). No-op on standard
	// Linux/macOS/Windows.
	_ "github.com/juex-ai/juex/internal/netbootstrap"
)

func main() {
	if handled, err := sandbox.MaybeExecTarget(os.Args); handled {
		if err != nil {
			fmt.Fprintln(os.Stderr, "Error:", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(cli.Execute())
}
