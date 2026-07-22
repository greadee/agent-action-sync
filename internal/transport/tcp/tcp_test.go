package tcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"syncgate/internal/core"
)

func TestTransportConnectsAndExchangesBytes(t *testing.T) {
	serverTLS := testTLSConfig(t, "SERVER")
	clientTLS := testTLSConfig(t, "CLIENT")
	serverTLS.ClientAuth = tls.RequireAnyClientCert
	clientTLS.InsecureSkipVerify = true

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	errs := make(chan error, 1)
	go func() {
		session := newSession(tls.Server(serverConn, serverTLS), "")
		stream, err := session.AcceptStream(context.Background())
		if err != nil {
			errs <- err
			return
		}
		if session.RemoteDeviceID() != "CLIENT" {
			errs <- nil
			return
		}
		_, err = stream.Write([]byte("pong"))
		errs <- err
	}()

	session := newSession(tls.Client(clientConn, clientTLS), core.DeviceID("SERVER"))
	stream, err := session.OpenStream(context.Background())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := stream.Read(buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("buf = %q", buf)
	}
	if err := <-errs; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func testTLSConfig(t *testing.T, commonName string) *tls.Config {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}
}
