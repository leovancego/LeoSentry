package api

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type allowPassword struct{}

func (allowPassword) CheckPassword(context.Context, string) (bool, error) { return true, nil }
func (allowPassword) SetPassword(context.Context, string) error           { return nil }

func TestHTTPAndHTTPS(t *testing.T) {
	certFile, keyFile := writeCert(t, "example.com")
	httpAddr, httpsAddr := freeAddr(t), freeAddr(t)
	srv := newTestServer(t, Options{
		Addrs:       []string{httpAddr},
		TLSAddrs:    []string{httpsAddr},
		TLSCertFile: certFile,
		TLSKeyFile:  keyFile,
		Passwords:   allowPassword{},
	})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- srv.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errc:
			if err != nil {
				t.Errorf("server: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("shutdown timeout")
		}
	})

	httpClient := &http.Client{Timeout: time.Second}
	httpsClient := tlsClient(t, certFile, "example.com")
	waitGET(t, httpClient, "http://"+httpAddr+"/")
	waitGET(t, httpsClient, "https://"+httpsAddr+"/")

	httpCookie := loginCookie(t, httpClient, "http://"+httpAddr+"/api/v1/login")
	if httpCookie.Secure {
		t.Fatal("HTTP 登录发出的会话 Cookie 带了 Secure，浏览器不会在 HTTP 上保存它")
	}
	httpsCookie := loginCookie(t, httpsClient, "https://"+httpsAddr+"/api/v1/login")
	if !httpsCookie.Secure || !httpsCookie.HttpOnly {
		t.Fatalf("HTTPS 会话 Cookie = %+v", httpsCookie)
	}
}

func TestMissingCertKeepsHTTP(t *testing.T) {
	dir := t.TempDir()
	httpAddr, httpsAddr := freeAddr(t), freeAddr(t)
	srv := newTestServer(t, Options{
		Addrs:       []string{httpAddr},
		TLSAddrs:    []string{httpsAddr},
		TLSCertFile: filepath.Join(dir, "missing.crt"),
		TLSKeyFile:  filepath.Join(dir, "missing.key"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- srv.Run(ctx) }()

	waitGET(t, &http.Client{Timeout: time.Second}, "http://"+httpAddr+"/")
	probe := &http.Client{
		Timeout: 200 * time.Millisecond,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		}},
	}
	if _, err := probe.Get("https://" + httpsAddr + "/"); err == nil {
		t.Fatal("证书缺失时不应监听 HTTPS")
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timeout")
	}
}

func newTestServer(t *testing.T, opts Options) *Server {
	t.Helper()
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func waitGET(t *testing.T, client *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			last = errStatus(resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("GET %s: %v", url, last)
}

type errStatus int

func (e errStatus) Error() string { return http.StatusText(int(e)) }

func loginCookie(t *testing.T, client *http.Client, url string) *http.Cookie {
	t.Helper()
	resp, err := client.Post(url, "application/json", strings.NewReader(`{"password":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login %s: %s", url, resp.Status)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("login response has no session cookie")
	return nil
}

func tlsClient(t *testing.T, certFile, serverName string) *http.Client {
	t.Helper()
	pemBytes, err := os.ReadFile(certFile)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("parse cert")
	}
	return &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			ServerName: serverName,
		}},
	}
}

func writeCert(t *testing.T, dnsName string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
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

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}
