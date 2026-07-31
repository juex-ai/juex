package mcp

import (
	"fmt"
	"net/http"
)

const maxSecureEndpointRedirects = 10

func newSecureEndpointHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Transport:     transport,
		CheckRedirect: checkSecureEndpointRedirect,
	}
}

func checkSecureEndpointRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= maxSecureEndpointRedirects {
		return fmt.Errorf("stopped after %d redirects", maxSecureEndpointRedirects)
	}
	if err := validateSecureEndpoint(request.URL.String()); err != nil {
		return fmt.Errorf("insecure redirect: %w", err)
	}
	return nil
}
