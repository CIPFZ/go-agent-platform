// Package tlsreload builds a TLS configuration that reloads a certificate
// pair for each new handshake, allowing atomic file rotation without restart.
package tlsreload

import (
	"crypto/tls"
	"errors"
	"strings"
)

func NewConfig(certFile, keyFile string) (*tls.Config, error) {
	if strings.TrimSpace(certFile) == "" || strings.TrimSpace(keyFile) == "" {
		return nil, errors.New("TLS certificate and key files are required")
	}
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{pair},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			current, err := tls.LoadX509KeyPair(certFile, keyFile)
			if err != nil {
				return nil, err
			}
			return &current, nil
		},
	}, nil
}
