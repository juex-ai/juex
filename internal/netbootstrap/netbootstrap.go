// Package netbootstrap installs network fallbacks for environments where
// the standard system configuration files Go expects (resolv.conf, CA
// bundle) are missing — most commonly Termux on Android, or other
// minimal distributions.
//
// The blank import of golang.org/x/crypto/x509roots/fallback registers a
// Mozilla NSS root pool that the crypto/x509 stdlib uses only when the
// system root pool is empty. The DNS install applies a custom Dial to
// net.DefaultResolver only when /etc/resolv.conf is unreadable. Both
// behaviours are no-ops on standard Linux/macOS/Windows and add roughly
// 250 KB to the binary.
package netbootstrap

import (
	_ "golang.org/x/crypto/x509roots/fallback"
)

func init() {
	Install()
}
