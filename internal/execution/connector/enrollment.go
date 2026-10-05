package connector

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"os"

	"github.com/juex-ai/juex/internal/execution"
)

// Enrollment is private executor configuration. Managed Host and explicitly
// paired devices use the same connector and journal format.
type Enrollment struct {
	Server        string           `json:"server"`
	Credential    string           `json:"credential"`
	Device        execution.Device `json:"device"`
	InsecureHTTP  bool             `json:"insecure_http"`
	HomeDirectory string           `json:"home_directory,omitempty"`
	CAFile        string           `json:"ca_file,omitempty"`
	ServerName    string           `json:"server_name,omitempty"`
}

func (e Enrollment) HTTPClient() (*http.Client, error) {
	if e.CAFile == "" {
		if e.ServerName != "" {
			return nil, errors.New("executor server identity requires a configured CA")
		}
		return nil, nil
	}
	data, err := os.ReadFile(e.CAFile)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("invalid executor endpoint CA")
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: e.ServerName}}}, nil
}
