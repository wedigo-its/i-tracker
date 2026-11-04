// internal/selfupdate/trust.go
// Why this exists: new installations accept a manifest only if it chains to the wedigo it solutions
// root (internal/updatesig). There is deliberately no fallback to the single release key in
// latest.json.sig: whoever controls the feed could otherwise just drop the CA signature. That file
// stays in the feed for installations that were shipped before the CA.
// Version: 2026.11.04
package selfupdate

import (
	"time"

	"i-tracker/internal/updatesig"
)

// ManifestPurpose binds a manifest signature to this product: a validly signed manifest of another
// wedigo product does not pass here.
var ManifestPurpose = updatesig.Purpose("update-manifest", "i-tracker")

// Trust returns the root this binary was built with, nil if it is unusable; an Updater without a
// root is disabled.
func Trust() *updatesig.Trust {
	t, err := updatesig.Embedded()
	if err != nil {
		return nil
	}
	return t
}

// verifyManifest checks the signature bundle and remembers the highest revocation list seen.
func (u *Updater) verifyManifest(manifest, bundle []byte) error {
	floorPath := ""
	if exe, err := executable(); err == nil {
		floorPath = exe + ".update-crl"
	}
	res, err := u.trust.Verify(bundle, ManifestPurpose, manifest, time.Now(), updatesig.ReadFloor(floorPath))
	if err != nil {
		return err
	}
	_ = updatesig.WriteFloor(floorPath, res.CRLNumber)
	return nil
}
