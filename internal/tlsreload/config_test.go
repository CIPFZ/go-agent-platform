package tlsreload

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writePair(t *testing.T, dir string, serial int64) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), cert, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), private, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCertificateReloadsAfterAtomicRotation(t *testing.T) {
	dir := t.TempDir()
	writePair(t, dir, 1)
	config, err := NewConfig(filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	firstParsed, err := x509.ParseCertificate(first.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	writePair(t, dir, 2)
	second, err := config.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	secondParsed, err := x509.ParseCertificate(second.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if firstParsed.SerialNumber.Cmp(secondParsed.SerialNumber) == 0 {
		t.Fatalf("certificate did not reload: serial=%s", secondParsed.SerialNumber)
	}
}
