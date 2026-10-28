// selfupdate_test.go
// Tests for manifest verification, binary download checks and executable swap
// Version: 2026.10.28

package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"i-tracker/internal/i18n"
)

const testProgram = "i-tracker"

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

// feed is a fake release feed. Every field can be changed per test before the
// first request is made.
type feed struct {
	manifest  []byte
	signature string
	binary    []byte
	sigStatus int
	downloads atomic.Int32
}

func signedFeed(t *testing.T, priv ed25519.PrivateKey, m Manifest) *feed {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return &feed{
		manifest:  data,
		signature: base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)),
		sigStatus: http.StatusOK,
	}
}

func (f *feed) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	f.handle(mux, "")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// handle registers the feed as the channel folder dir ("" for the root).
func (f *feed) handle(mux *http.ServeMux, dir string) {
	prefix := "/"
	if dir != "" {
		prefix = "/" + dir + "/"
	}
	mux.HandleFunc("GET "+prefix+"latest.json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(f.manifest)
	})
	mux.HandleFunc("GET "+prefix+"latest.json.sig", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(f.sigStatus)
		_, _ = w.Write([]byte(f.signature + "\n"))
	})
	mux.HandleFunc("GET "+prefix+"bin", func(w http.ResponseWriter, r *http.Request) {
		f.downloads.Add(1)
		if f.binary == nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(f.binary)
	})
}

func platformKey(program string) string {
	return runtime.GOOS + "/" + runtime.GOARCH + "/" + program
}

func digest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// Check

func TestCheckAcceptsSignedManifest(t *testing.T) {
	pub, priv := newKey(t)
	f := signedFeed(t, priv, Manifest{Version: "1.4.0", Notes: "fixes"})
	srv := f.serve(t)
	// A trailing slash in the configured feed URL must not break the paths.
	u := New(srv.URL+"/", pub)

	rel, err := u.Check(context.Background(), "1.3.9")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !rel.Available || rel.Manifest.Version != "1.4.0" || rel.Manifest.Notes != "fixes" {
		t.Fatalf("Check = %+v, want available release 1.4.0", rel)
	}

	rel, err = u.Check(context.Background(), "1.4.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel.Available {
		t.Fatal("same version reported as an available update")
	}
}

func TestCheckRejectsUntrustedManifest(t *testing.T) {
	pub, priv := newKey(t)
	_, otherPriv := newKey(t)

	cases := map[string]struct {
		mutate  func(f *feed)
		wantErr string // catalog key
	}{
		"tampered manifest": {func(f *feed) {
			f.manifest = []byte(strings.Replace(string(f.manifest), "1.4.0", "9.9.9", 1))
		}, "err.update.bad_signature"},
		"signed by other key": {func(f *feed) {
			f.signature = base64.StdEncoding.EncodeToString(ed25519.Sign(otherPriv, f.manifest))
		}, "err.update.bad_signature"},
		"empty signature":     {func(f *feed) { f.signature = "" }, "err.update.bad_signature"},
		"malformed signature": {func(f *feed) { f.signature = "%%% not base64 %%%" }, "err.update.decode_signature"},
		"missing signature":   {func(f *feed) { f.sigStatus = http.StatusNotFound }, "err.update.fetch_signature"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := signedFeed(t, priv, Manifest{Version: "1.4.0"})
			tc.mutate(f)
			rel, err := New(f.serve(t).URL, pub).Check(context.Background(), "1.0.0")
			if err == nil {
				t.Fatalf("Check accepted an untrusted manifest: %+v", rel)
			}
			if i18n.KeyOf(err) != tc.wantErr {
				t.Fatalf("error = %v (key %q), want key %q", err, i18n.KeyOf(err), tc.wantErr)
			}
		})
	}
}

func TestCheckRejectsSignedButMalformedManifest(t *testing.T) {
	pub, priv := newKey(t)
	f := signedFeed(t, priv, Manifest{})
	f.manifest = []byte("{not json")
	f.signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, f.manifest))

	if _, err := New(f.serve(t).URL, pub).Check(context.Background(), "1.0.0"); err == nil {
		t.Fatal("Check accepted a manifest that is not valid JSON")
	}
}

func TestUpdaterDisabledWithoutFeedOrKey(t *testing.T) {
	pub, _ := newKey(t)
	cases := map[string]*Updater{
		"nil updater": nil,
		"no feed":     New("", pub),
		"short key":   New("https://updates.example.com", pub[:16]),
		"no key":      New("https://updates.example.com", nil),
	}
	for name, u := range cases {
		t.Run(name, func(t *testing.T) {
			if u.Enabled() {
				t.Fatal("Enabled() = true")
			}
			if _, err := u.Check(context.Background(), "1.0.0"); err == nil {
				t.Fatal("Check succeeded on a disabled updater")
			}
		})
	}
}

// Apply and Rollback

// useExecutable points Apply and Rollback at a stand-in binary holding
// content, so a failing test can never replace the test binary itself.
func useExecutable(t *testing.T, content string) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "i-tracker.exe")
	writeFile(t, exe, content)
	orig := executable
	executable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { executable = orig })
	return exe
}

func TestApplyInstallsVerifiedBinaryAndRollsBack(t *testing.T) {
	pub, priv := newKey(t)
	exe := useExecutable(t, "old")
	payload := []byte("new binary")
	m := Manifest{Version: "2.0.0", Binaries: map[string]Binary{platformKey(testProgram): {SHA256: digest(payload)}}}
	f := signedFeed(t, priv, m)
	f.binary = payload
	srv := f.serve(t)
	bin := m.Binaries[platformKey(testProgram)]
	bin.URL = srv.URL + "/bin"
	m.Binaries[platformKey(testProgram)] = bin

	if err := New(srv.URL, pub).Apply(context.Background(), m, testProgram); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, exe); got != string(payload) {
		t.Fatalf("executable = %q, want the downloaded binary", got)
	}
	if got := readFile(t, exe+".bak"); got != "old" {
		t.Fatalf("backup = %q, want the previous binary", got)
	}

	if err := Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := readFile(t, exe); got != "old" {
		t.Errorf("executable after rollback = %q, want the previous binary", got)
	}
	if got := readFile(t, exe+".bak"); got != string(payload) {
		t.Errorf("backup after rollback = %q, want the rolled-back binary", got)
	}
}

func TestApplyStopsBeforeInstall(t *testing.T) {
	pub, priv := newKey(t)
	payload := []byte("new binary")

	cases := map[string]struct {
		manifest      Manifest
		binary        []byte
		wantErr       string
		wantDownloads int32
	}{
		"no binary for platform": {
			manifest:      Manifest{Version: "2.0.0", Binaries: map[string]Binary{"plan9/mips/" + testProgram: {}}},
			wantErr:       "err.update.no_binary",
			wantDownloads: 0,
		},
		"download fails": {
			manifest:      Manifest{Version: "2.0.0", Binaries: map[string]Binary{platformKey(testProgram): {SHA256: digest(payload)}}},
			binary:        nil,
			wantErr:       "err.update.download",
			wantDownloads: 1,
		},
		"checksum mismatch": {
			manifest:      Manifest{Version: "2.0.0", Binaries: map[string]Binary{platformKey(testProgram): {SHA256: digest([]byte("other"))}}},
			binary:        payload,
			wantErr:       "err.update.checksum",
			wantDownloads: 1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			exe := useExecutable(t, "current")
			f := signedFeed(t, priv, tc.manifest)
			f.binary = tc.binary
			srv := f.serve(t)
			for key, bin := range tc.manifest.Binaries {
				bin.URL = srv.URL + "/bin"
				tc.manifest.Binaries[key] = bin
			}

			err := New(srv.URL, pub).Apply(context.Background(), tc.manifest, testProgram)
			if err == nil || i18n.KeyOf(err) != tc.wantErr {
				t.Fatalf("Apply error = %v (key %q), want key %q", err, i18n.KeyOf(err), tc.wantErr)
			}
			if got := f.downloads.Load(); got != tc.wantDownloads {
				t.Errorf("downloads = %d, want %d", got, tc.wantDownloads)
			}
			if got := readFile(t, exe); got != "current" {
				t.Errorf("executable = %q, want it untouched", got)
			}
			if _, err := os.Stat(exe + ".bak"); err == nil {
				t.Error("Apply moved the executable aside despite failing")
			}
		})
	}
}

// replaceExecutable

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReplaceExecutableSwapsAndKeepsBackup(t *testing.T) {
	// Run from an unrelated directory so a stray temp file would not land in
	// the package directory.
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	exe := filepath.Join(dir, "i-tracker.exe")
	writeFile(t, exe, "old")
	writeFile(t, exe+".bak", "older")

	if err := replaceExecutable(exe, []byte("new")); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}
	if got := readFile(t, exe); got != "new" {
		t.Errorf("executable = %q, want new", got)
	}
	if got := readFile(t, exe+".bak"); got != "old" {
		t.Errorf("backup = %q, want the previous binary", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(exe)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("new executable mode = %v, want it executable", info.Mode().Perm())
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".i-tracker-update-*"))
	if len(leftovers) > 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestReplaceExecutableStagesNextToBinary(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := t.TempDir()
	exe := filepath.Join(dir, "i-tracker.exe")
	writeFile(t, exe, "old")

	var stagedIn []string
	orig := createTemp
	createTemp = func(d, pattern string) (*os.File, error) {
		stagedIn = append(stagedIn, d)
		return orig(d, pattern)
	}
	t.Cleanup(func() { createTemp = orig })

	if err := replaceExecutable(exe, []byte("new")); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}
	// Staging elsewhere (e.g. the working directory) breaks the final rename
	// when that directory lies on another volume.
	if len(stagedIn) != 1 || stagedIn[0] != dir {
		t.Fatalf("update staged in %q, want the executable's directory %q", stagedIn, dir)
	}
}

func TestReplaceExecutableFailsWithoutExecutable(t *testing.T) {
	t.Chdir(t.TempDir())
	exe := filepath.Join(t.TempDir(), "missing.exe")

	if err := replaceExecutable(exe, []byte("new")); err == nil {
		t.Fatal("replaceExecutable succeeded although there was nothing to replace")
	}
	if _, err := os.Stat(exe); err == nil {
		t.Error("new binary installed without a backup of the previous one")
	}
}

// Rollback

func TestRestoreBackupSwapsBinaries(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "i-tracker.exe")
	writeFile(t, exe, "broken")
	writeFile(t, exe+".bak", "previous")

	if err := restoreBackup(exe); err != nil {
		t.Fatalf("restoreBackup: %v", err)
	}
	if got := readFile(t, exe); got != "previous" {
		t.Errorf("executable = %q, want the backup", got)
	}
	// Keeping the replaced binary lets the operator undo the rollback.
	if got := readFile(t, exe+".bak"); got != "broken" {
		t.Errorf("backup = %q, want the replaced binary", got)
	}
	if _, err := os.Stat(exe + ".rollback"); err == nil {
		t.Error("intermediate file left behind")
	}
}

func TestRestoreBackupWithoutBackup(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "i-tracker.exe")
	writeFile(t, exe, "current")

	if err := restoreBackup(exe); err == nil {
		t.Fatal("restoreBackup succeeded without a backup")
	}
	if got := readFile(t, exe); got != "current" {
		t.Errorf("executable = %q, want it untouched", got)
	}
}

// TestHelperProcess is not a real test: the tests below start it as a child
// process so they can swap a binary that is in use.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("ITRACKER_SELFUPDATE_HELPER") != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

// startRunning copies the test binary to exe and runs it until the test ends.
func startRunning(t *testing.T, exe string) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, image, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "ITRACKER_SELFUPDATE_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return image
}

// Windows cannot overwrite or delete a running executable, which is exactly
// the situation of both an update and a rollback started from the service.
func TestSwapsWorkWhileBinaryRuns(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		exe := filepath.Join(t.TempDir(), "i-tracker.exe")
		image := startRunning(t, exe)

		if err := replaceExecutable(exe, []byte("new")); err != nil {
			t.Fatalf("replaceExecutable on a running binary: %v", err)
		}
		if got := readFile(t, exe); got != "new" {
			t.Errorf("executable = %q, want new", got)
		}
		if got := readFile(t, exe+".bak"); got != string(image) {
			t.Error("backup does not hold the running binary")
		}
	})
	t.Run("rollback", func(t *testing.T) {
		exe := filepath.Join(t.TempDir(), "i-tracker.exe")
		image := startRunning(t, exe)
		writeFile(t, exe+".bak", "previous")

		if err := restoreBackup(exe); err != nil {
			t.Fatalf("restoreBackup on a running binary: %v", err)
		}
		if got := readFile(t, exe); got != "previous" {
			t.Errorf("executable = %q, want the backup", got)
		}
		if got := readFile(t, exe+".bak"); got != string(image) {
			t.Error("backup does not hold the replaced binary")
		}
	})
}

// Version comparison

func TestNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"1.2.4", "1.2.3", true},
		{"1.10.0", "1.9.9", true}, // numeric, not lexical
		{"2.0.0", "1.99.99", true},
		{"v1.2.4", "1.2.3", true},
		{"1.2.3.1", "1.2.3", true},
		{"1.0.0", "0.0.0-dev", true}, // development builds see every release
		{"1.2.3", "1.2.3", false},
		{"v1.2.3", "1.2.3", false},
		{"1.2.2", "1.2.3", false},
		{"1.2", "1.2.3", false},
		{"1.2.3-rc1", "1.2.3", false}, // suffixes are ignored
		{"1.2.3+build5", "1.2.3", false},
		// Malformed versions never trigger an update.
		{"", "1.0.0", false},
		{"latest", "1.0.0", false},
		{"1.x.0", "1.0.0", false},
		{"2.0.0", "unknown", false},
	}
	for _, tc := range cases {
		if got := newer(tc.candidate, tc.current); got != tc.want {
			t.Errorf("newer(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.want)
		}
	}
}
