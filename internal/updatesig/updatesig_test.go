// internal/updatesig/updatesig_test.go
// Why this exists: every way an update signature must be refused, with hierarchies made at runtime,
// and the proof that the embedded root is a real self-signed root.
// Version: 2026.11.04
package updatesig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPurpose = "update-manifest/test"

var (
	testNow  = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	testData = []byte(`{"version":"v1.2.3"}`)
)

func hierarchy(t *testing.T) (Hierarchy, *Trust) {
	t.Helper()
	h, err := NewHierarchy(testNow)
	if err != nil {
		t.Fatal(err)
	}
	trust, err := h.Trust()
	if err != nil {
		t.Fatal(err)
	}
	return h, trust
}

func sign(t *testing.T, s Signer, purpose string, data []byte) []byte {
	t.Helper()
	bundle, err := s.Sign(purpose, data)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestVerifyAcceptsSignedData(t *testing.T) {
	h, trust := hierarchy(t)
	res, err := trust.Verify(sign(t, h.Signer, testPurpose, testData), testPurpose, testData, testNow, 0)
	if err != nil {
		t.Fatalf("Verify = %v", err)
	}
	if res.CRLNumber != 1 {
		t.Fatalf("CRLNumber = %d, want 1", res.CRLNumber)
	}
}

func TestRejectWrongDataOrPurpose(t *testing.T) {
	h, trust := hierarchy(t)
	bundle := sign(t, h.Signer, testPurpose, testData)
	if _, err := trust.Verify(bundle, testPurpose, []byte("other"), testNow, 0); !errors.Is(err, ErrBadSignature) {
		t.Errorf("changed data: %v, want %v", err, ErrBadSignature)
	}
	if _, err := trust.Verify(bundle, "update-manifest/other", testData, testNow, 0); !errors.Is(err, ErrBadSignature) {
		t.Errorf("another product: %v, want %v", err, ErrBadSignature)
	}
}

func TestRejectForeignHierarchy(t *testing.T) {
	foreign, _ := hierarchy(t)
	_, trust := hierarchy(t)
	if _, err := trust.Verify(sign(t, foreign.Signer, testPurpose, testData), testPurpose, testData, testNow, 0); !errors.Is(err, ErrUntrustedSigner) {
		t.Fatalf("Verify = %v, want %v", err, ErrUntrustedSigner)
	}
}

func TestRejectKeyThatIsNotTheCertificates(t *testing.T) {
	h, trust := hierarchy(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := h.Signer
	s.Key = other
	if _, err := trust.Verify(sign(t, s, testPurpose, testData), testPurpose, testData, testNow, 0); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify = %v, want %v", err, ErrBadSignature)
	}
}

func TestRejectExpiredSigningCertificate(t *testing.T) {
	h, trust := hierarchy(t)
	later := testNow.Add(SigningLifetime + time.Hour)
	crl, err := NewCRL(h.RootDER, h.RootKey, 2, nil, later, CRLLifetime)
	if err != nil {
		t.Fatal(err)
	}
	s := h.Signer
	s.CRLDER = crl
	if _, err := trust.Verify(sign(t, s, testPurpose, testData), testPurpose, testData, later, 0); !errors.Is(err, ErrUntrustedSigner) {
		t.Fatalf("Verify = %v, want %v", err, ErrUntrustedSigner)
	}
}

func TestRejectRevokedSigningCertificate(t *testing.T) {
	h, trust := hierarchy(t)
	cert, err := x509.ParseCertificate(h.Signer.CertDER)
	if err != nil {
		t.Fatal(err)
	}
	crl, err := NewCRL(h.RootDER, h.RootKey, 2, []*big.Int{cert.SerialNumber}, testNow, CRLLifetime)
	if err != nil {
		t.Fatal(err)
	}
	s := h.Signer
	s.CRLDER = crl
	if _, err := trust.Verify(sign(t, s, testPurpose, testData), testPurpose, testData, testNow, 0); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Verify = %v, want %v", err, ErrRevoked)
	}
}

// The replacement signing certificate must work with the list that revokes the old one.
func TestReplacementWorksAfterRevocation(t *testing.T) {
	h, trust := hierarchy(t)
	old, err := x509.ParseCertificate(h.Signer.CertDER)
	if err != nil {
		t.Fatal(err)
	}
	certDER, key, err := NewSigningCert(h.RootDER, h.RootKey, testNow, SigningLifetime)
	if err != nil {
		t.Fatal(err)
	}
	crl, err := NewCRL(h.RootDER, h.RootKey, 2, []*big.Int{old.SerialNumber}, testNow, CRLLifetime)
	if err != nil {
		t.Fatal(err)
	}
	s := Signer{Key: key, CertDER: certDER, CRLDER: crl}
	if _, err := trust.Verify(sign(t, s, testPurpose, testData), testPurpose, testData, testNow, 1); err != nil {
		t.Fatalf("Verify = %v", err)
	}
}

func TestRejectExpiredRevocationList(t *testing.T) {
	h, trust := hierarchy(t)
	later := testNow.Add(CRLLifetime + time.Hour)
	_, err := trust.Verify(sign(t, h.Signer, testPurpose, testData), testPurpose, testData, later, 0)
	if !errors.Is(err, ErrBadCRL) {
		t.Fatalf("Verify = %v, want %v", err, ErrBadCRL)
	}
}

func TestRejectOlderRevocationList(t *testing.T) {
	h, trust := hierarchy(t)
	_, err := trust.Verify(sign(t, h.Signer, testPurpose, testData), testPurpose, testData, testNow, 2)
	if !errors.Is(err, ErrCRLRollback) {
		t.Fatalf("Verify = %v, want %v", err, ErrCRLRollback)
	}
}

// A signing key must not be able to write its own revocation list.
func TestRejectRevocationListNotFromRoot(t *testing.T) {
	h, trust := hierarchy(t)
	foreign, _ := hierarchy(t)
	s := h.Signer
	s.CRLDER = foreign.Signer.CRLDER
	if _, err := trust.Verify(sign(t, s, testPurpose, testData), testPurpose, testData, testNow, 0); !errors.Is(err, ErrBadCRL) {
		t.Fatalf("Verify = %v, want %v", err, ErrBadCRL)
	}
}

// issuedByRoot lets a test issue a certificate that breaks one rule of the signing certificate.
func issuedByRoot(t *testing.T, h Hierarchy, edit func(*x509.Certificate)) Signer {
	t.Helper()
	root, err := x509.ParseCertificate(h.RootDER)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(77),
		Subject:               pkix.Name{CommonName: "other"},
		NotBefore:             testNow.Add(-time.Hour),
		NotAfter:              testNow.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
	}
	edit(tmpl)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, root, pub, h.RootKey)
	if err != nil {
		t.Fatal(err)
	}
	return Signer{Key: priv, CertDER: der, CRLDER: h.Signer.CRLDER}
}

func TestRejectCertificateForOtherUse(t *testing.T) {
	h, trust := hierarchy(t)
	cases := map[string]func(*x509.Certificate){
		"server certificate": func(c *x509.Certificate) { c.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth} },
		"may not sign":       func(c *x509.Certificate) { c.KeyUsage = x509.KeyUsageCertSign; c.IsCA = true },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			s := issuedByRoot(t, h, edit)
			if _, err := trust.Verify(sign(t, s, testPurpose, testData), testPurpose, testData, testNow, 0); !errors.Is(err, ErrUntrustedSigner) {
				t.Fatalf("Verify = %v, want %v", err, ErrUntrustedSigner)
			}
		})
	}
}

func TestRejectMalformedBundle(t *testing.T) {
	h, trust := hierarchy(t)
	good := sign(t, h.Signer, testPurpose, testData)
	block := func(kind string, b []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: b}) }
	cases := map[string][]byte{
		"empty":            nil,
		"not PEM":          []byte("%%% no pem %%%"),
		"trailing data":    append(bytes.Clone(good), []byte("tail")...),
		"second signature": append(bytes.Clone(good), block(blockSignature, make([]byte, ed25519.SignatureSize))...),
		"foreign block":    append(bytes.Clone(good), block("PRIVATE KEY", []byte{1})...),
		"oversized":        bytes.Repeat([]byte{'x'}, maxBundle+1),
		"no certificate":   append(block(blockCRL, h.Signer.CRLDER), block(blockSignature, make([]byte, ed25519.SignatureSize))...),
		"no revocation":    append(block(blockCertificate, h.Signer.CertDER), block(blockSignature, make([]byte, ed25519.SignatureSize))...),
		"no signature":     append(block(blockCertificate, h.Signer.CertDER), block(blockCRL, h.Signer.CRLDER)...),
		"short signature":  append(append(block(blockCertificate, h.Signer.CertDER), block(blockCRL, h.Signer.CRLDER)...), block(blockSignature, []byte{1, 2})...),
	}
	for name, bundle := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := trust.Verify(bundle, testPurpose, testData, testNow, 0); !errors.Is(err, ErrBadBundle) {
				t.Fatalf("Verify = %v, want %v", err, ErrBadBundle)
			}
		})
	}
}

func TestRejectWithoutTrust(t *testing.T) {
	var trust *Trust
	if _, err := trust.Verify(nil, testPurpose, testData, testNow, 0); !errors.Is(err, ErrNoRoot) {
		t.Fatalf("Verify = %v, want %v", err, ErrNoRoot)
	}
	if _, err := NewTrust([]byte("no certificate")); !errors.Is(err, ErrNoRoot) {
		t.Fatalf("NewTrust = %v, want %v", err, ErrNoRoot)
	}
}

// The self-signature proves whoever embedded this root held its private key.
func TestEmbeddedRootIsTheWedigoRoot(t *testing.T) {
	trust, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded = %v", err)
	}
	for _, root := range trust.roots {
		if got := root.Subject.CommonName; !strings.HasPrefix(got, RootCommonName) {
			t.Fatalf("CommonName = %q, want the prefix %q", got, RootCommonName)
		}
		if !root.IsCA || root.KeyUsage&x509.KeyUsageCRLSign == 0 {
			t.Fatalf("root may not issue: IsCA %v, KeyUsage %v", root.IsCA, root.KeyUsage)
		}
	}
	if strings.Contains(rootPEM, "PRIVATE") {
		t.Fatal("rootPEM holds a private key")
	}
}

// successor is the rollover in one place: a second root with its own signing certificate.
func successor(t *testing.T, now time.Time) Hierarchy {
	t.Helper()
	rootDER, rootKey, err := NewRoot(RootCommonName+" G2", now)
	if err != nil {
		t.Fatal(err)
	}
	certDER, key, err := NewSigningCert(rootDER, rootKey, now, SigningLifetime)
	if err != nil {
		t.Fatal(err)
	}
	crlDER, err := NewCRL(rootDER, rootKey, 7, nil, now, CRLLifetime)
	if err != nil {
		t.Fatal(err)
	}
	return Hierarchy{RootDER: rootDER, RootKey: rootKey, Signer: Signer{Key: key, CertDER: certDER, CRLDER: crlDER}}
}

// The order of a root rollover: a binary with both roots accepts either chain, so the signing
// certificate can move at any time, while a binary with the old root alone refuses the new chain.
func TestRootRollover(t *testing.T) {
	old, onlyOld := hierarchy(t)
	next := successor(t, testNow)
	both, err := NewTrust(old.RootDER, next.RootDER)
	if err != nil {
		t.Fatal(err)
	}
	oldBundle := sign(t, old.Signer, testPurpose, testData)
	newBundle := sign(t, next.Signer, testPurpose, testData)

	if _, err := both.Verify(oldBundle, testPurpose, testData, testNow, 0); err != nil {
		t.Errorf("both roots, old chain: %v", err)
	}
	if res, err := both.Verify(newBundle, testPurpose, testData, testNow, 1); err != nil || res.CRLNumber != 7 {
		t.Errorf("both roots, new chain: %+v, %v", res, err)
	}
	if _, err := onlyOld.Verify(newBundle, testPurpose, testData, testNow, 0); !errors.Is(err, ErrUntrustedSigner) {
		t.Errorf("old root alone, new chain: %v, want %v", err, ErrUntrustedSigner)
	}
	// The list counts on across roots, so a feed cannot fall back to the old root's list.
	if _, err := both.Verify(oldBundle, testPurpose, testData, testNow, 7); !errors.Is(err, ErrCRLRollback) {
		t.Errorf("old list after the new one: %v, want %v", err, ErrCRLRollback)
	}
}

// A list of one root says nothing about a certificate of the other.
func TestRejectRevocationListOfTheOtherRoot(t *testing.T) {
	old, _ := hierarchy(t)
	next := successor(t, testNow)
	both, err := NewTrust(old.RootDER, next.RootDER)
	if err != nil {
		t.Fatal(err)
	}
	s := next.Signer
	s.CRLDER = old.Signer.CRLDER
	if _, err := both.Verify(sign(t, s, testPurpose, testData), testPurpose, testData, testNow, 0); !errors.Is(err, ErrBadCRL) {
		t.Fatalf("Verify = %v, want %v", err, ErrBadCRL)
	}
}

// The successor must keep updates working after the old root has ended.
func TestSuccessorWorksAfterOldRootEnded(t *testing.T) {
	old, _ := hierarchy(t)
	later := testNow.Add(RootLifetime + time.Hour)
	next := successor(t, later)
	both, err := NewTrust(old.RootDER, next.RootDER)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := both.Verify(sign(t, next.Signer, testPurpose, testData), testPurpose, testData, later, 0); err != nil {
		t.Fatalf("new chain after the old root ended: %v", err)
	}
	if _, err := both.Verify(sign(t, old.Signer, testPurpose, testData), testPurpose, testData, later, 0); !errors.Is(err, ErrUntrustedSigner) {
		t.Fatalf("old chain after the old root ended: %v, want %v", err, ErrUntrustedSigner)
	}
}

// The signing certificate lives at least ten years, and so does the root.
func TestLifetimesMeetTheDecision(t *testing.T) {
	const tenYears = 10 * 365 * 24 * time.Hour
	if SigningLifetime < tenYears || RootLifetime < tenYears {
		t.Fatalf("signing %v, root %v, want at least %v", SigningLifetime, RootLifetime, tenYears)
	}
	h, _ := hierarchy(t)
	cert, err := x509.ParseCertificate(h.Signer.CertDER)
	if err != nil {
		t.Fatal(err)
	}
	if got := cert.NotAfter.Sub(testNow); got < tenYears {
		t.Fatalf("issued signing certificate lives %v", got)
	}
}

func TestLastRootNotAfterIsTheLongestLived(t *testing.T) {
	old, _ := hierarchy(t)
	later := testNow.Add(5 * 365 * 24 * time.Hour)
	next := successor(t, later)
	both, err := NewTrust(old.RootDER, next.RootDER)
	if err != nil {
		t.Fatal(err)
	}
	if want := later.Add(RootLifetime); !both.LastRootNotAfter().Equal(want) {
		t.Fatalf("LastRootNotAfter = %v, want %v", both.LastRootNotAfter(), want)
	}
	if got := len(both.RootsDER()); got != 2 {
		t.Fatalf("RootsDER = %d, want 2", got)
	}
}

func TestLoadSignerRefusesForeignFolder(t *testing.T) {
	h, _ := hierarchy(t)
	dir := t.TempDir()
	key, err := EncodeKey(h.Signer.Key)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		SigningKeyFile:  key,
		SigningCertFile: EncodeCert(h.Signer.CertDER),
		CRLFile:         EncodeCRL(h.Signer.CRLDER),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := LoadSigner(dir, testNow); !errors.Is(err, ErrUntrustedSigner) {
		t.Fatalf("LoadSigner = %v, want %v", err, ErrUntrustedSigner)
	}
}

func TestFloorOnlyRises(t *testing.T) {
	path := filepath.Join(t.TempDir(), "floor")
	if got := ReadFloor(path); got != 0 {
		t.Fatalf("missing file = %d, want 0", got)
	}
	for _, step := range []struct{ write, want int64 }{{3, 3}, {2, 3}, {5, 5}} {
		if err := WriteFloor(path, step.write); err != nil {
			t.Fatal(err)
		}
		if got := ReadFloor(path); got != step.want {
			t.Fatalf("after writing %d: %d, want %d", step.write, got, step.want)
		}
	}
	if err := os.WriteFile(path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadFloor(path); got != 0 {
		t.Fatalf("garbage = %d, want 0", got)
	}
}

func TestExpiryWarning(t *testing.T) {
	h, _ := hierarchy(t)
	if got := h.Signer.expiryWarning(testNow); got != "" {
		t.Fatalf("fresh hierarchy warns: %q", got)
	}
	soon := testNow.Add(CRLLifetime - 30*24*time.Hour)
	if got := h.Signer.expiryWarning(soon); !strings.Contains(got, "revocation list") {
		t.Fatalf("warning = %q", got)
	}
}
