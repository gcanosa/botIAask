package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// GenerateClientCert makes a self-signed ECDSA P-256 certificate (10 years) and returns the
// PEM bundle (certificate + private key) plus its SHA-256 fingerprint for NickServ CERT ADD.
func GenerateClientCert(nick string) (bundle, fingerprint string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		return "", "", err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nick},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	bundle = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) +
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	return bundle, fmt.Sprintf("%x", sha256.Sum256(der)), nil
}

// ClientCertFingerprint validates a PEM bundle (cert + matching key) and returns the
// SHA-256 fingerprint of the leaf certificate.
func ClientCertFingerprint(bundle string) (string, error) {
	pair, err := tls.X509KeyPair([]byte(bundle), []byte(bundle))
	if err != nil {
		return "", fmt.Errorf("need a PEM certificate and its private key: %w", err)
	}
	sum := sha256.Sum256(pair.Certificate[0])
	return hex.EncodeToString(sum[:]), nil
}
