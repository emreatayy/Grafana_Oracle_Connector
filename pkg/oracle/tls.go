package oracle

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/emreatayy/oracle-datasource/pkg/models"
)

// BuildTLSConfig turns the datasource TLS settings into the tls.Config that
// go-ora applies when it wraps the socket in tls.Client at connect time
// (go-ora v2.9.0 network/session.go). A nil config means "no TLS", the default,
// so existing plain deployments are unchanged. Failures — an unknown mode or an
// unusable custom CA — are hard errors: a misconfigured certificate must never
// silently downgrade the session to plaintext (report §16).
func BuildTLSConfig(s *models.OracleSettings) (*tls.Config, error) {
	switch s.TLSMode {
	case "":
		return nil, nil
	case "skip-verify":
		// Operator opt-in for self-signed test racks: encrypt the session but
		// skip certificate validation. Never the default.
		return &tls.Config{InsecureSkipVerify: true}, nil // #nosec G402 — operator opt-in
	case "custom-ca":
		pool, err := certPool(s.TLSCAPEM)
		if err != nil {
			return nil, err
		}
		return &tls.Config{RootCAs: pool}, nil
	default:
		return nil, fmt.Errorf("unknown tlsMode %q (want '', 'skip-verify' or 'custom-ca')", s.TLSMode)
	}
}

// certPool builds a verification pool from the configured PEM bundle, layered
// on top of the system roots so the pinned CA adds to them instead of replacing
// them. An empty bundle or one with no parseable certificates is an error; a
// public CA in place of the server's own CA is also valid, since the pool is
// used purely for server verification.
func certPool(pemText string) (*x509.CertPool, error) {
	if pemText == "" {
		return nil, fmt.Errorf("tlsCaPem is required when tlsMode is custom-ca")
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM([]byte(pemText)) {
		return nil, fmt.Errorf("tlsCaPem contains no valid PEM certificates")
	}
	return pool, nil
}