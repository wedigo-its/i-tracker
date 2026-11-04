// internal/updatesig/updatesig.go
// Why this exists: wedigo products must accept an update only if it chains to the wedigo it
// solutions root, so that a lost signing key can be replaced and revoked from the offline root
// instead of being a pinned key baked into every installation. Identical copy in every product;
// the source is _docs/ca/updatesig, whose test fails on any difference.
// Version: 2026.11.04
package updatesig

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// BundleSuffix is appended to the signed file's name: latest.json.casig.
const BundleSuffix = ".casig"

const (
	blockCertificate = "CERTIFICATE"
	blockCRL         = "X509 CRL"
	blockSignature   = "WEDIGO SIGNATURE"

	// maxBundle bounds what is parsed from the network before anything is trusted.
	maxBundle = 64 << 10
	// clockTolerance keeps a client whose clock runs behind working.
	clockTolerance = 24 * time.Hour
)

var (
	ErrNoRoot          = errors.New("no wedigo root certificate")
	ErrBadBundle       = errors.New("malformed signature bundle")
	ErrUntrustedSigner = errors.New("signing certificate does not chain to the wedigo root")
	ErrRevoked         = errors.New("signing certificate is revoked")
	ErrBadCRL          = errors.New("revocation list is not valid")
	ErrCRLRollback     = errors.New("revocation list is older than one seen before")
	ErrBadSignature    = errors.New("signature does not verify")
)

// Purpose names what a signature is for and for which product, and is part of the signed message:
// a manifest of one product must not pass as a manifest of another.
func Purpose(kind, product string) string { return kind + "/" + product }

// message is what actually gets signed. The prefix keeps these signatures apart from raw Ed25519
// signatures made with the same key.
func message(purpose string, data []byte) []byte {
	const domain = "wedigo-update-signature-v1\x00"
	msg := make([]byte, 0, len(domain)+len(purpose)+1+len(data))
	msg = append(msg, domain...)
	msg = append(msg, purpose...)
	msg = append(msg, 0)
	return append(msg, data...)
}

// Trust is a set of verified root certificates. More than one is the normal state while a root is
// being replaced: a release ships the successor next to the current root, and only later does the
// signing certificate move below the successor. See the rollover section of RELEASE-UPDATES.md.
type Trust struct{ roots []*x509.Certificate }

// Embedded returns the roots this binary was built with.
func Embedded() (*Trust, error) {
	var ders [][]byte
	for rest := []byte(rootPEM); ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		ders = append(ders, block.Bytes)
	}
	return NewTrust(ders...)
}

// NewTrust checks that each certificate is a self-signed Ed25519 CA.
func NewTrust(ders ...[]byte) (*Trust, error) {
	if len(ders) == 0 {
		return nil, ErrNoRoot
	}
	t := &Trust{}
	for _, der := range ders {
		root, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNoRoot, err)
		}
		if _, ok := root.PublicKey.(ed25519.PublicKey); !ok {
			return nil, fmt.Errorf("%w: key of type %T, want Ed25519", ErrNoRoot, root.PublicKey)
		}
		if err := root.CheckSignatureFrom(root); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNoRoot, err)
		}
		t.roots = append(t.roots, root)
	}
	return t, nil
}

// RootsDER returns the roots, oldest first.
func (t *Trust) RootsDER() [][]byte {
	out := make([][]byte, 0, len(t.roots))
	for _, root := range t.roots {
		out = append(out, root.Raw)
	}
	return out
}

// LastRootNotAfter reports when the longest-lived root expires: from then on these binaries can
// accept no update at all, so a successor has to reach them well before.
func (t *Trust) LastRootNotAfter() time.Time {
	var last time.Time
	for _, root := range t.roots {
		if root.NotAfter.After(last) {
			last = root.NotAfter
		}
	}
	return last
}

// CheckSigner verifies a signing certificate against the root at now and returns its key. It does
// not look at revocation; Verify does.
func (t *Trust) CheckSigner(der []byte, now time.Time) (ed25519.PublicKey, error) {
	cert, _, err := t.checkSigner(der, now)
	if err != nil {
		return nil, err
	}
	return cert.PublicKey.(ed25519.PublicKey), nil
}

// checkSigner returns the certificate and the root it chains to. The revocation list has to come
// from that same root: a list of one root says nothing about what the other one issued.
func (t *Trust) checkSigner(der []byte, now time.Time) (*x509.Certificate, *x509.Certificate, error) {
	if t == nil || len(t.roots) == 0 {
		return nil, nil, ErrNoRoot
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrUntrustedSigner, err)
	}
	if cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return nil, nil, fmt.Errorf("%w: not allowed to sign", ErrUntrustedSigner)
	}
	if _, ok := cert.PublicKey.(ed25519.PublicKey); !ok {
		return nil, nil, fmt.Errorf("%w: key of type %T, want Ed25519", ErrUntrustedSigner, cert.PublicKey)
	}
	var lastErr error
	for _, root := range t.roots {
		pool := x509.NewCertPool()
		pool.AddCert(root)
		if _, lastErr = cert.Verify(x509.VerifyOptions{
			Roots:       pool,
			CurrentTime: now,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		}); lastErr == nil {
			return cert, root, nil
		}
	}
	return nil, nil, fmt.Errorf("%w: %w", ErrUntrustedSigner, lastErr)
}

// checkCRL verifies the revocation list against the signer's root and returns its number. An expired list
// fails on purpose: it is what stops a stolen signing key being used with an old list for ever.
func checkCRL(root *x509.Certificate, der []byte, serial string, now time.Time, floor int64) (int64, error) {
	crl, err := x509.ParseRevocationList(der)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrBadCRL, err)
	}
	if err := crl.CheckSignatureFrom(root); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrBadCRL, err)
	}
	if now.After(crl.NextUpdate) {
		return 0, fmt.Errorf("%w: expired on %s", ErrBadCRL, crl.NextUpdate.UTC().Format(time.DateOnly))
	}
	if now.Add(clockTolerance).Before(crl.ThisUpdate) {
		return 0, fmt.Errorf("%w: not valid before %s", ErrBadCRL, crl.ThisUpdate.UTC().Format(time.DateOnly))
	}
	if crl.Number == nil || !crl.Number.IsInt64() || crl.Number.Sign() < 0 {
		return 0, fmt.Errorf("%w: no usable number", ErrBadCRL)
	}
	number := crl.Number.Int64()
	if number < floor {
		return 0, fmt.Errorf("%w: number %d, seen %d", ErrCRLRollback, number, floor)
	}
	for _, entry := range crl.RevokedCertificateEntries {
		if entry.SerialNumber.Text(16) == serial {
			return 0, fmt.Errorf("%w: serial %s", ErrRevoked, serial)
		}
	}
	return number, nil
}

// Result describes a verified bundle.
type Result struct {
	CRLNumber int64
}

// Verify checks bundle as a signature over data for purpose: signing certificate, revocation list
// and signature. crlFloor is the highest list number this installation has seen, see ReadFloor.
func (t *Trust) Verify(bundle []byte, purpose string, data []byte, now time.Time, crlFloor int64) (Result, error) {
	if t == nil || len(t.roots) == 0 {
		return Result{}, ErrNoRoot
	}
	certDER, crlDER, sig, err := parseBundle(bundle)
	if err != nil {
		return Result{}, err
	}
	cert, root, err := t.checkSigner(certDER, now)
	if err != nil {
		return Result{}, err
	}
	number, err := checkCRL(root, crlDER, cert.SerialNumber.Text(16), now, crlFloor)
	if err != nil {
		return Result{}, err
	}
	if !ed25519.Verify(cert.PublicKey.(ed25519.PublicKey), message(purpose, data), sig) {
		return Result{}, ErrBadSignature
	}
	return Result{CRLNumber: number}, nil
}

// parseBundle accepts exactly one certificate, one revocation list and one signature.
func parseBundle(bundle []byte) (cert, crl, sig []byte, err error) {
	if len(bundle) > maxBundle {
		return nil, nil, nil, fmt.Errorf("%w: %d bytes", ErrBadBundle, len(bundle))
	}
	rest := bundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		var slot *[]byte
		switch block.Type {
		case blockCertificate:
			slot = &cert
		case blockCRL:
			slot = &crl
		case blockSignature:
			slot = &sig
		default:
			return nil, nil, nil, fmt.Errorf("%w: unexpected block %q", ErrBadBundle, block.Type)
		}
		if *slot != nil {
			return nil, nil, nil, fmt.Errorf("%w: second block %q", ErrBadBundle, block.Type)
		}
		*slot = block.Bytes
	}
	if len(strings.TrimSpace(string(rest))) != 0 {
		return nil, nil, nil, fmt.Errorf("%w: trailing data", ErrBadBundle)
	}
	if cert == nil || crl == nil || sig == nil {
		return nil, nil, nil, fmt.Errorf("%w: certificate, revocation list and signature are all required", ErrBadBundle)
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, nil, nil, fmt.Errorf("%w: signature has %d bytes", ErrBadBundle, len(sig))
	}
	return cert, crl, sig, nil
}

// ReadFloor returns the highest revocation list number stored at path, 0 if there is none.
func ReadFloor(path string) int64 {
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// WriteFloor raises the stored number to n. Keeping it stops a feed handing out an older list once
// a newer one was seen. A program folder that is read-only only loses that protection, not updates.
func WriteFloor(path string, n int64) error {
	if path == "" || n <= ReadFloor(path) {
		return nil
	}
	return os.WriteFile(path, []byte(strconv.FormatInt(n, 10)+"\n"), 0o644)
}
