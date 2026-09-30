package platformrpc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// CreateCredentials initializes an operator-owned directory. It refuses to
// replace existing trust or keys; leaf keys are mounted only into their service.
func CreateCredentials(directory string) error {
	if directory == "" {
		return errors.New("certificate output directory is required")
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return err
	}
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "JueX platform services"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return err
	}
	if err := writePair(directory, "ca", der, key); err != nil {
		return err
	}
	for _, role := range []string{"management", "runtime", "execution", "memory", "calendar"} {
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return err
		}
		serial, err := randomSerial()
		if err != nil {
			return err
		}
		leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: role}, DNSNames: []string{role}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
		encoded, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		if err != nil {
			return err
		}
		if err := writePair(directory, role, encoded, leafKey); err != nil {
			return err
		}
	}
	return nil
}

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

func writePair(directory, name string, certificate []byte, key *ecdsa.PrivateKey) error {
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	for _, file := range []struct {
		name, kind string
		data       []byte
	}{{name + ".pem", "CERTIFICATE", certificate}, {name + ".key", "PRIVATE KEY", encoded}} {
		f, err := os.OpenFile(filepath.Join(directory, file.name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		err = pem.Encode(f, &pem.Block{Type: file.kind, Bytes: file.data})
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
