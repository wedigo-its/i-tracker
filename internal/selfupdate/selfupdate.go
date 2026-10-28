// selfupdate.go
// Signed release feed, verified download and in-place swap of the binary
// Version: 2026.10.28

// Package selfupdate implements the in-place updater for the i-Tracker
// binary, ported from Spherifyer and Acmefyer. A release feed on
// www.wedigo.net publishes a signed manifest; the Manager checks it
// periodically and, in the default "auto" mode, installs a newer release on
// its own and restarts the service. "notify" only reports a release, "off"
// disables the updater.
//
// Integrity is guaranteed by an Ed25519 signature over the release manifest,
// verified against the embedded wedigo release key (ReleasePublicKey), plus a
// per-binary SHA-256 digest. The previous binary is kept as ".bak": a version
// that does not come up healthy is rolled back automatically (see state.go),
// and "i-tracker rollback" restores it on request.
//
// Unlike Spherifyer and Acmefyer, i-Tracker has one channel for all versions
// (see ChannelURL): there is no license per major version, so every release
// is served from the feed root and an update may cross a major version.
package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"i-tracker/internal/i18n"
)

// DefaultFeedURL is the release feed; no setting redirects it.
const DefaultFeedURL = "https://www.wedigo.net/update/i-tracker"

// releasePublicKeyB64 is the wedigo release signing key, the same in every
// wedigo product. It is separate from the license key, so a leak of one does
// not compromise the other.
const releasePublicKeyB64 = "qatPqYMAVrMDsPOrCYm5QXq9wV9KBTFMZqYM0olq/UI="

// ReleasePublicKey returns the embedded key release manifests are verified
// against.
func ReleasePublicKey() ed25519.PublicKey {
	key, err := base64.StdEncoding.DecodeString(releasePublicKeyB64)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil
	}
	return ed25519.PublicKey(key)
}

// Manifest is the signed release descriptor served at latest.json. Fields of
// the other products' manifests (major, next_major) are ignored.
type Manifest struct {
	Version  string            `json:"version"`
	Notes    string            `json:"notes,omitempty"`
	Binaries map[string]Binary `json:"binaries"` // key: "<goos>/<goarch>/<program>"
}

// Binary is a single downloadable artifact and its expected SHA-256 digest.
type Binary struct {
	// URL is absolute or relative to the channel URL.
	URL    string `json:"url"`
	SHA256 string `json:"sha256"` // hex-encoded
}

// Release is the result of a successful, verified check.
type Release struct {
	Manifest  Manifest
	Available bool // true when the manifest version is newer than the running one
}

// ChannelURL returns the folder below feedURL whose latest.json a binary
// reporting version current follows. Spherifyer and Acmefyer serve each major
// version from its own folder (v2/, v3/ ...) because a license covers one
// major version only. i-Tracker has no license: every version (0.x, 1.x and
// any later major) follows the feed root, so current is deliberately unused.
func ChannelURL(feedURL, current string) string {
	return strings.TrimRight(feedURL, "/")
}

// Filesystem hooks, replaceable in tests: executable locates the binary that
// Apply and Rollback replace, createTemp reveals where an update is staged.
var (
	executable = startExecutable
	createTemp = os.CreateTemp
)

// startExe is the binary's path as the process started. os.Executable asked
// later follows the running file: on Linux, after Apply renamed it to *.bak,
// it named the backup, so the update state went to the backup's .update.json
// and the new version never found it (no start counting, no confirmation, no
// automatic rollback).
var startExe, startExeErr = os.Executable()

func startExecutable() (string, error) { return startExe, startExeErr }

// Updater checks a feed and applies updates.
type Updater struct {
	feedURL string
	pubKey  ed25519.PublicKey
	client  *http.Client
}

// New returns an Updater for the given feed base URL and verification key.
func New(feedURL string, pubKey ed25519.PublicKey) *Updater {
	return &Updater{
		feedURL: strings.TrimRight(feedURL, "/"),
		pubKey:  pubKey,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Enabled reports whether self-update is configured.
func (u *Updater) Enabled() bool {
	return u != nil && u.feedURL != "" && len(u.pubKey) == ed25519.PublicKeySize
}

// FeedURL returns the feed base URL.
func (u *Updater) FeedURL() string {
	if u == nil {
		return ""
	}
	return u.feedURL
}

// ChannelURL returns the channel a binary reporting version current follows.
func (u *Updater) ChannelURL(current string) string {
	if u == nil || u.feedURL == "" {
		return ""
	}
	return ChannelURL(u.feedURL, current)
}

// Check fetches and verifies the release manifest and reports whether it is
// newer than currentVersion.
func (u *Updater) Check(ctx context.Context, currentVersion string) (Release, error) {
	if !u.Enabled() {
		return Release{}, i18n.E("err.update.not_configured")
	}
	channel := u.ChannelURL(currentVersion)
	manifestBytes, err := u.get(ctx, channel+"/latest.json")
	if err != nil {
		return Release{}, i18n.Wrap(err, "err.update.fetch_manifest")
	}
	sigBytes, err := u.get(ctx, channel+"/latest.json.sig")
	if err != nil {
		return Release{}, i18n.Wrap(err, "err.update.fetch_signature")
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigBytes)))
	if err != nil {
		return Release{}, i18n.Wrap(err, "err.update.decode_signature")
	}
	if !ed25519.Verify(u.pubKey, manifestBytes, sig) {
		return Release{}, i18n.E("err.update.bad_signature")
	}
	var m Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		return Release{}, i18n.Wrap(err, "err.update.decode_manifest")
	}
	return Release{Manifest: m, Available: newer(m.Version, currentVersion)}, nil
}

// Apply downloads the binary for the current platform and program, verifies its
// digest and atomically replaces the running executable, keeping a ".bak" backup
// for rollback. The caller is responsible for restarting the process.
func (u *Updater) Apply(ctx context.Context, m Manifest, program string) error {
	key := runtime.GOOS + "/" + runtime.GOARCH + "/" + program
	bin, ok := m.Binaries[key]
	if !ok {
		return i18n.E("err.update.no_binary", key, m.Version)
	}
	data, err := u.get(ctx, u.resolve(bin.URL, m.Version))
	if err != nil {
		return i18n.Wrap(err, "err.update.download")
	}
	h := sha256.Sum256(data)
	if got := hex.EncodeToString(h[:]); !strings.EqualFold(got, bin.SHA256) {
		return i18n.E("err.update.checksum", bin.SHA256, got)
	}

	exe, err := executable()
	if err != nil {
		return err
	}
	return replaceExecutable(exe, data)
}

// resolve turns a manifest URL into an absolute one. Relative URLs are taken
// relative to the channel of the manifest, so a feed can be moved or mirrored
// as a whole.
func (u *Updater) resolve(ref, version string) string {
	if strings.Contains(ref, "://") {
		return ref
	}
	return ChannelURL(u.feedURL, version) + "/" + strings.TrimLeft(ref, "/")
}

// replaceExecutable writes data next to the running binary and swaps it in,
// keeping the previous version as <exe>.bak. The write-then-rename keeps the
// operation atomic on the same filesystem; staging anywhere else (such as the
// working directory, System32 for a Windows service) breaks the rename when
// that lies on another volume.
func replaceExecutable(exe string, data []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := createTemp(dir, ".i-tracker-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}

	backup := exe + ".bak"
	_ = os.Remove(backup)
	// Move the current binary aside (Windows cannot overwrite a running exe, but
	// it can rename it), then move the new one into place.
	if err := os.Rename(exe, backup); err != nil {
		return i18n.Wrap(err, "err.update.backup")
	}
	if err := os.Rename(tmpName, exe); err != nil {
		// Roll back the rename so the service can still start.
		_ = os.Rename(backup, exe)
		return i18n.Wrap(err, "err.update.install_binary")
	}
	return nil
}

// Writable reports whether the folder of the running binary accepts a staged
// update. It does not when a package manager owns the binary (/usr/bin under a
// hardened systemd unit) or the service account lacks the rights.
func Writable() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	tmp, err := createTemp(filepath.Dir(exe), ".i-tracker-probe-*")
	if err != nil {
		return err
	}
	tmp.Close()
	return os.Remove(tmp.Name())
}

// Rollback restores the previous binary from its ".bak" backup; the binary it
// replaces becomes the new backup. It takes effect on the next start.
func Rollback() error {
	exe, err := executable()
	if err != nil {
		return err
	}
	if err := restoreBackup(exe); err != nil {
		return err
	}
	// A manual rollback ends any pending automatic one.
	clearPending()
	return nil
}

// restoreBackup swaps exe with exe.bak. It only renames and never overwrites:
// Windows refuses to replace an executable that is running (Rollback itself
// runs from it) but allows renaming it.
func restoreBackup(exe string) error {
	backup := exe + ".bak"
	if _, err := os.Stat(backup); err != nil {
		return i18n.E("err.update.no_backup")
	}
	aside := exe + ".rollback"
	_ = os.Remove(aside)
	if err := os.Rename(exe, aside); err != nil {
		return i18n.Wrap(err, "err.update.move_aside")
	}
	if err := os.Rename(backup, exe); err != nil {
		_ = os.Rename(aside, exe)
		return i18n.Wrap(err, "err.update.restore")
	}
	if err := os.Rename(aside, backup); err != nil {
		return i18n.Wrap(err, "err.update.restore_partial", aside)
	}
	return nil
}

func (u *Updater) get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, i18n.E("err.update.http", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 128<<20))
}

// IsRelease reports whether v is a clean release version such as "1.4.2" or
// "v1.4.2". Development builds ("dev", a commit hash, "1.4.2-3-gabc1234",
// "-dirty") are not, and are never replaced automatically. The dot rules out
// a short commit hash that happens to be all digits.
func IsRelease(v string) bool {
	v = strings.TrimSpace(v)
	return parseVersion(v) != nil && strings.Contains(v, ".") && !strings.ContainsAny(v, "-+")
}

// newer reports whether candidate is a strictly newer semantic-ish version than
// current. It compares dot-separated numeric components and treats a leading "v"
// and any "-suffix" as noise. Unparsable versions compare as not-newer so a
// malformed feed never triggers an update.
func newer(candidate, current string) bool {
	c := parseVersion(candidate)
	cur := parseVersion(current)
	if c == nil || cur == nil {
		return false
	}
	for i := 0; i < len(c) && i < len(cur); i++ {
		if c[i] != cur[i] {
			return c[i] > cur[i]
		}
	}
	return len(c) > len(cur)
}

func parseVersion(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			return nil
		}
		n := 0
		for _, r := range p {
			if r < '0' || r > '9' {
				return nil
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}
