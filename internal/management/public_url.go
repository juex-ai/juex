package management

import (
	"errors"
	"net/url"
)

// PublicOrigin validates the origin used by browser CSRF checks and capability links.
func PublicOrigin(raw string, insecureHTTP bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") ||
		(u.Scheme != "https" && (u.Scheme != "http" || !insecureHTTP)) {
		return "", errors.New("public URL must be an HTTPS origin (HTTP requires explicit development mode)")
	}
	return u.Scheme + "://" + u.Host, nil
}
