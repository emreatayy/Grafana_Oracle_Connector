package oracle

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/emreatayy/oracle-datasource/pkg/models"
)

// fixture is a self-signed leaf cert (with a DNS SAN) plus its PEM, so tests
// can act as both the CA bundle and an Oracle-style TLS server.
type fixture struct {
	caPEM string
	cert  tls.Certificate
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "oracle.test"},
		DNSNames:              []string{"oracle.test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return fixture{
		caPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		cert:  tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
	}
}

func TestBuildTLSConfig_Disabled(t *testing.T) {
	cfg, err := BuildTLSConfig(&models.OracleSettings{TLSMode: ""})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if cfg != nil {
		t.Fatalf("config = %+v, want nil for the default plain connection", cfg)
	}
}

func TestBuildTLSConfig_SkipVerify(t *testing.T) {
	cfg, err := BuildTLSConfig(&models.OracleSettings{TLSMode: "skip-verify"})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if cfg == nil || !cfg.InsecureSkipVerify {
		t.Fatalf("config = %+v, want InsecureSkipVerify=true", cfg)
	}

	// The point of the mode: a handshake against an untrusted self-signed
	// server still succeeds.
	fx := newFixture(t)
	ln := tlsTestServer(t, fx.cert, nil)
	client := &tls.Config{InsecureSkipVerify: true} // #nosec G402 — test exercises skip-verify
	if err := tlsHandshake(ln, client); err != nil {
		t.Fatalf("skip-verify handshake failed: %v", err)
	}
}

func TestBuildTLSConfig_CustomCA(t *testing.T) {
	fx := newFixture(t)
	cfg, err := BuildTLSConfig(&models.OracleSettings{TLSMode: "custom-ca", TLSCAPEM: fx.caPEM})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if cfg == nil || cfg.RootCAs == nil {
		t.Fatalf("config = %+v, want RootCAs pool from the fixture CA", cfg)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("custom-ca must not disable verification")
	}

	// A server presenting the pinned self-signed cert verifies cleanly…
	ln := tlsTestServer(t, fx.cert, nil)
	if err := tlsHandshake(ln, &tls.Config{RootCAs: cfg.RootCAs, ServerName: "oracle.test"}); err != nil {
		t.Fatalf("custom-ca handshake failed: %v", err)
	}

	// …while a client without the CA in its pool is rejected.
	empty := tls.Config{RootCAs: x509.NewCertPool(), ServerName: "oracle.test"}
	if err := tlsHandshake(ln, &empty); err == nil {
		t.Fatal("handshake with an empty pool succeeded, want certificate rejection")
	}
}

func TestBuildTLSConfig_CustomCAErrors(t *testing.T) {
	for _, s := range []*models.OracleSettings{
		{TLSMode: "custom-ca", TLSCAPEM: ""},
		{TLSMode: "custom-ca", TLSCAPEM: "not a pem"},
		{TLSMode: "bogus"},
	} {
		if _, err := BuildTLSConfig(s); err == nil {
			t.Errorf("settings %+v: expected error, got nil", s)
		}
	}
}

// tlsTestServer starts an in-memory TLS listener serving cert (any client, no
// client auth) and returns it. An accept loop runs as long as the listener is
// open, so each caller handshake has a server side to complete against.
func tlsTestServer(t *testing.T, cert tls.Certificate, clientCA *x509.CertPool) net.Listener {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: clientCA})
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				close(done)
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				if tconn, ok := conn.(*tls.Conn); ok {
					_ = tconn.Handshake()
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return ln
}

// tlsHandshake dials the listener's address with clientCfg and performs a full
// handshake against the accept loop's server side.
func tlsHandshake(ln net.Listener, clientCfg *tls.Config) error {
	conn, err := tls.Dial("tcp", ln.Addr().String(), clientCfg)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.Handshake()
}