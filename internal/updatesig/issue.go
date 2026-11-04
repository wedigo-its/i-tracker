// internal/updatesig/issue.go
// Why this exists: the feed tools and the CA tool must make exactly what Verify accepts, and tests
// need a whole hierarchy at runtime instead of a key on disk. Binaries that only verify do not link
// any of it.
// Version: 2026.11.04
package updatesig

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	Organization      = "wedigo it solutions"
	RootCommonName    = "wedigo it solutions Root CA"
	SigningCommonName = "wedigo software"

	RootLifetime    = 30 * 365 * 24 * time.Hour
	SigningLifetime = 10 * 365 * 24 * time.Hour
	CRLLifetime     = 400 * 24 * time.Hour

	// RootWarnBefore is how long before the last root ends the tools ask for a successor: the
	// binaries that carry it have to reach installations while the old root still works.
	RootWarnBefore = 5 * 365 * 24 * time.Hour

	// CRLWarnBefore is how long before the end the feed tools start warning.
	CRLWarnBefore = 60 * 24 * time.Hour

	RootKeyFile     = "wedigo-root-ca.key"
	RootCertFile    = "wedigo-root-ca.crt"
	NextRootKeyFile = "wedigo-root-ca-next.key"
	NextRootCert    = "wedigo-root-ca-next.crt"
	CRLFile         = "wedigo-root-ca.crl"
	SigningKeyFile  = "wedigo-software-ca.key"
	SigningCertFile = "wedigo-software-ca.crt"
)

// Signer makes signature bundles with the signing key.
type Signer struct {
	Key     ed25519.PrivateKey
	CertDER []byte
	CRLDER  []byte
}

// Sign returns the signature bundle over data for purpose.
func (s Signer) Sign(purpose string, data []byte) ([]byte, error) {
	if len(s.Key) != ed25519.PrivateKeySize || len(s.CertDER) == 0 || len(s.CRLDER) == 0 {
		return nil, errors.New("signer needs a key, a certificate and a revocation list")
	}
	var out []byte
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: blockCertificate, Bytes: s.CertDER})...)
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: blockCRL, Bytes: s.CRLDER})...)
	out = append(out, pem.EncodeToMemory(&pem.Block{Type: blockSignature, Bytes: ed25519.Sign(s.Key, message(purpose, data))})...)
	return out, nil
}

// LoadSigner reads the signing key, its certificate and the revocation list from dir and proves them
// against the embedded root, so a feed is never built that the shipped binaries would reject. The
// returned warning is empty unless the list or the certificate will soon need renewing.
func LoadSigner(dir string, now time.Time) (Signer, string, error) {
	trust, err := Embedded()
	if err != nil {
		return Signer{}, "", err
	}
	key, err := readKey(filepath.Join(dir, SigningKeyFile))
	if err != nil {
		return Signer{}, "", err
	}
	certDER, err := readPEM(filepath.Join(dir, SigningCertFile), blockCertificate)
	if err != nil {
		return Signer{}, "", err
	}
	crlDER, err := readPEM(filepath.Join(dir, CRLFile), blockCRL)
	if err != nil {
		return Signer{}, "", err
	}
	s := Signer{Key: key, CertDER: certDER, CRLDER: crlDER}
	const probePurpose = "probe"
	bundle, err := s.Sign(probePurpose, nil)
	if err != nil {
		return Signer{}, "", err
	}
	if _, err := trust.Verify(bundle, probePurpose, nil, now, 0); err != nil {
		return Signer{}, "", fmt.Errorf("%s: %w", dir, err)
	}
	if trust.LastRootNotAfter().Sub(now) < RootWarnBefore {
		return s, "the last embedded root expires on " + trust.LastRootNotAfter().UTC().Format(time.DateOnly) + "; ship a successor in a release (root-add)", nil
	}
	return s, s.expiryWarning(now), nil
}

func (s Signer) expiryWarning(now time.Time) string {
	crl, err := x509.ParseRevocationList(s.CRLDER)
	if err != nil {
		return ""
	}
	if crl.NextUpdate.Sub(now) < CRLWarnBefore {
		return "revocation list expires on " + crl.NextUpdate.UTC().Format(time.DateOnly) + "; clients stop accepting updates then, renew it with the root"
	}
	cert, err := x509.ParseCertificate(s.CertDER)
	if err != nil {
		return ""
	}
	if cert.NotAfter.Sub(now) < CRLWarnBefore {
		return "signing certificate expires on " + cert.NotAfter.UTC().Format(time.DateOnly) + "; renew it with the root"
	}
	return ""
}

// Hierarchy is a root with its signing certificate and revocation list.
type Hierarchy struct {
	RootDER []byte
	RootKey ed25519.PrivateKey
	Signer  Signer
}

// Trust returns the hierarchy's root as a Trust.
func (h Hierarchy) Trust() (*Trust, error) { return NewTrust(h.RootDER) }

// NewHierarchy creates a root, a signing certificate and a first revocation list.
func NewHierarchy(now time.Time) (Hierarchy, error) {
	rootDER, rootKey, err := NewRoot(RootCommonName, now)
	if err != nil {
		return Hierarchy{}, err
	}
	certDER, key, err := NewSigningCert(rootDER, rootKey, now, SigningLifetime)
	if err != nil {
		return Hierarchy{}, err
	}
	crlDER, err := NewCRL(rootDER, rootKey, 1, nil, now, CRLLifetime)
	if err != nil {
		return Hierarchy{}, err
	}
	return Hierarchy{RootDER: rootDER, RootKey: rootKey, Signer: Signer{Key: key, CertDER: certDER, CRLDER: crlDER}}, nil
}

func newSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
}

// NewRoot creates the self-signed root. It may issue certificates and revocation lists, nothing else.
func NewRoot(commonName string, now time.Time) ([]byte, ed25519.PrivateKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{Organization}, CommonName: commonName},
		NotBefore:             now.Add(-clockTolerance),
		NotAfter:              now.Add(RootLifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, nil, err
	}
	return der, priv, nil
}

// NewSigningCert issues the intermediate that signs updates. Its extended key usage limits it to
// code signing, and it may not issue further CAs.
func NewSigningCert(rootDER []byte, rootKey ed25519.PrivateKey, now time.Time, lifetime time.Duration) ([]byte, ed25519.PrivateKey, error) {
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{Organization: []string{Organization}, CommonName: SigningCommonName},
		NotBefore:             now.Add(-clockTolerance),
		NotAfter:              now.Add(lifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root, pub, rootKey)
	if err != nil {
		return nil, nil, err
	}
	return der, priv, nil
}

// NewCRL makes a revocation list signed by the root.
func NewCRL(rootDER []byte, rootKey ed25519.PrivateKey, number int64, revoked []*big.Int, now time.Time, lifetime time.Duration) ([]byte, error) {
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return nil, err
	}
	entries := make([]x509.RevocationListEntry, 0, len(revoked))
	for _, serial := range revoked {
		entries = append(entries, x509.RevocationListEntry{SerialNumber: serial, RevocationTime: now})
	}
	return x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:                    big.NewInt(number),
		ThisUpdate:                now.Add(-clockTolerance),
		NextUpdate:                now.Add(lifetime),
		RevokedCertificateEntries: entries,
	}, root, rootKey)
}

// EncodeKey writes a private key as unencrypted PKCS#8 PEM.
func EncodeKey(priv ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// EncodeCert and EncodeCRL write the PEM forms of the files in the CA folder.
func EncodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: blockCertificate, Bytes: der})
}
func EncodeCRL(der []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: blockCRL, Bytes: der}) }

func readPEM(path, blockType string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != blockType {
		return nil, fmt.Errorf("%s: no PEM block of type %q", path, blockType)
	}
	return block.Bytes, nil
}

// ReadCert and ReadCRL read the PEM files of the CA folder.
func ReadCert(path string) ([]byte, error) { return readPEM(path, blockCertificate) }
func ReadCRL(path string) ([]byte, error)  { return readPEM(path, blockCRL) }

// ReadKey reads an unencrypted PKCS#8 PEM Ed25519 key.
func ReadKey(path string) (ed25519.PrivateKey, error) { return readKey(path) }

func readKey(path string) (ed25519.PrivateKey, error) {
	der, err := readPEM(path, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s: key of type %T, want Ed25519", path, key)
	}
	return priv, nil
}
