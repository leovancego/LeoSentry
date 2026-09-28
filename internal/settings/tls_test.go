package settings

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leo/leosentry/internal/config"
	"github.com/leo/leosentry/internal/store"
)

func TestCheckTLS(t *testing.T) {
	if err := CheckTLS("", ""); err != nil {
		t.Fatal(err)
	}
	if err := CheckTLS("/tmp/a.crt", ""); err == nil {
		t.Fatal("expected pair error")
	}
	if err := CheckTLS("cert.crt", "cert.key"); err == nil {
		t.Fatal("expected absolute path")
	}
	cert, key := writeCert(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err := CheckTLS(cert, key); err != nil {
		t.Fatal(err)
	}
	expired, expiredKey := writeCert(t, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	if err := CheckTLS(expired, expiredKey); err == nil || err.Error() != "证书已经过期" {
		t.Fatalf("expired = %v", err)
	}
	if err := CheckTLS(cert, filepath.Join(t.TempDir(), "missing.key")); err == nil {
		t.Fatal("expected missing file")
	}
}

func TestSaveTLSRestartFlag(t *testing.T) {
	db := openPolicy(t)
	svc := Bind(db, nil, nil, nil, nil, "03:00")
	svc.UseTLS("", "", 8088, 8443)
	ctx := context.Background()

	if _, restart, err := svc.SaveTLS(ctx, "", "", 8088, 8443); err != nil || restart {
		t.Fatalf("empty save restart=%v err=%v", restart, err)
	}
	cert, key := writeCert(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	view, restart, err := svc.SaveTLS(ctx, cert, key, 8088, 8443)
	if err != nil || !restart || view.CertFile != cert || view.Active {
		t.Fatalf("save view=%+v restart=%v err=%v", view.TLS, restart, err)
	}
	svc.NoteTLS(cert, key, "")
	view, restart, err = svc.SaveTLS(ctx, cert, key, 9090, 8443)
	if err != nil || !restart || view.HTTPPort != 9090 {
		t.Fatalf("port save view=%+v restart=%v err=%v", view.TLS, restart, err)
	}
	if _, _, err := svc.SaveTLS(ctx, cert, "", 8088, 8443); err == nil {
		t.Fatal("expected pair error")
	}
	if _, _, err := svc.SaveTLS(ctx, "", "", 8088, 8088); err == nil {
		t.Fatal("expected distinct ports")
	}
	if _, _, err := svc.SaveTLS(ctx, "", "", 0, 0); err == nil {
		t.Fatal("expected both-closed error")
	}
}

func TestParsePort(t *testing.T) {
	n, err := ParsePort("8443")
	if err != nil || n != 8443 {
		t.Fatalf("port=%d err=%v", n, err)
	}
	for _, raw := range []string{"", "8443.5", "abc", "-1", "65536", "08 88", "1e3"} {
		if _, err := ParsePort(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestApplyStoredTLSOverridesEmpty(t *testing.T) {
	db := openPolicy(t)
	ctx := context.Background()
	if err := db.SetSetting(ctx, keyTLSCert, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, keyTLSKey, ""); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.TLSCertFile = "/etc/ssl/certs/from-uci.crt"
	cfg.TLSKeyFile = "/etc/ssl/private/from-uci.key"
	got, err := ApplyStored(ctx, db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.TLSCertFile != "" || got.TLSKeyFile != "" {
		t.Fatalf("stored empty did not override: %+v", got.TLSCertFile)
	}
	if err := db.SetSetting(ctx, keyHTTPPort, "9090"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, keyHTTPSPort, "9443"); err != nil {
		t.Fatal(err)
	}
	got, err = ApplyStored(ctx, db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.HTTPPort != 9090 || got.HTTPSPort != 9443 {
		t.Fatalf("ports = %d %d", got.HTTPPort, got.HTTPSPort)
	}
}

func openPolicy(t *testing.T) *store.PolicyStore {
	t.Helper()
	db, err := store.OpenPolicy(context.Background(), filepath.Join(t.TempDir(), "policy.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func writeCert(t *testing.T, notBefore, notAfter time.Time) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		DNSNames:     []string{"example.com"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
