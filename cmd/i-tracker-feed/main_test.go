// main_test.go
// Tests for the feed tool: one channel at the feed root, version check of the binaries, signing key
// Version: 2026.10.28

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"i-tracker/internal/selfupdate"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// fakeBinary writes a stand-in binary for a foreign platform, which the tool
// checks by the embedded version string instead of running it.
func fakeBinary(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "i-tracker")
	if err := os.WriteFile(path, []byte("binary "+version+" end"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const foreignPlatform = "plan9/386/i-tracker"

// readSigned reads latest.json of dir and verifies its signature.
func readSigned(t *testing.T, dir string, pub ed25519.PublicKey) (selfupdate.Manifest, string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	sigText, err := os.ReadFile(filepath.Join(dir, "latest.json.sig"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigText)))
	if err != nil || !ed25519.Verify(pub, data, sig) {
		t.Fatalf("signature of %s does not verify", dir)
	}
	var m selfupdate.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m, string(data)
}

// Every version, 0.x included, lands in the feed root without channel fields.
func TestBuildWritesIntoFeedRoot(t *testing.T) {
	pub, priv := newKey(t)
	for _, version := range []string{"v0.0.1", "v1.2.3", "v2.0.0"} {
		t.Run(version, func(t *testing.T) {
			out := t.TempDir()
			dir, err := build(buildOptions{version: version, notes: "n", out: out, bins: []string{foreignPlatform + "=" + fakeBinary(t, version)}}, priv)
			if err != nil {
				t.Fatal(err)
			}
			if dir != out {
				t.Fatalf("manifest folder = %s, want the feed root %s", dir, out)
			}
			m, raw := readSigned(t, dir, pub)
			if m.Version != version || m.Notes != "n" {
				t.Fatalf("manifest = %+v", m)
			}
			if strings.Contains(raw, "major") {
				t.Errorf("manifest has channel fields: %s", raw)
			}
			bin := m.Binaries[foreignPlatform]
			if want := version + "/plan9-386/i-tracker"; bin.URL != want {
				t.Errorf("binary URL = %q, want %q relative to the feed root", bin.URL, want)
			}
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(bin.URL))); err != nil {
				t.Errorf("binary not copied: %v", err)
			}
		})
	}
}

func TestBuildRefuses(t *testing.T) {
	_, priv := newKey(t)
	good := fakeBinary(t, "v0.0.1")
	cases := map[string]buildOptions{
		"development version": {version: "v0.0.1-3-gabc1234", bins: []string{foreignPlatform + "=" + good}},
		"dirty version":       {version: "v0.0.1-dirty", bins: []string{foreignPlatform + "=" + good}},
		"binary of other tag": {version: "v0.0.2", bins: []string{foreignPlatform + "=" + good}},
		"one bad binary":      {version: "v0.0.1", bins: []string{foreignPlatform + "=" + good, "plan9/arm/i-tracker=" + fakeBinary(t, "v0.0.0")}},
		"malformed -bin":      {version: "v0.0.1", bins: []string{"plan9/i-tracker=" + good}},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			o.out = t.TempDir()
			if _, err := build(o, priv); err == nil {
				t.Fatal("build succeeded")
			}
			entries, _ := os.ReadDir(o.out)
			if len(entries) != 0 {
				t.Errorf("files written despite the error: %v", entries)
			}
		})
	}
}

// The feed must be signed with the key the binaries embed.
func TestReleaseKeyMustMatchEmbeddedKey(t *testing.T) {
	_, priv := newKey(t)
	file := filepath.Join(t.TempDir(), "throwaway.key")
	if err := os.WriteFile(file, []byte(base64.StdEncoding.EncodeToString(priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEDIGO_RELEASE_KEY", "")
	t.Setenv("WEDIGO_RELEASE_KEY_FILE", "")
	got, err := loadKey(file)
	if err != nil || !got.Equal(priv) {
		t.Fatalf("loadKey = %v", err)
	}
	if _, err := releaseKey(file); err == nil || !strings.Contains(err.Error(), "does not belong") {
		t.Fatalf("releaseKey accepted a foreign key: %v", err)
	}
	if _, err := loadKey(""); err == nil {
		t.Fatal("loadKey without a key succeeded")
	}
}

func TestDiscoverFindsBuildOutputs(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, p := range []string{"_release/i-tracker.exe", "_release/linux-amd64/i-tracker", "_release/linux-arm64/i-tracker"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.Join(discover(), "\n")
	for _, want := range []string{
		"windows/amd64/i-tracker=" + filepath.Join("_release", "i-tracker.exe"),
		"linux/amd64/i-tracker=" + filepath.Join("_release", "linux-amd64", "i-tracker"),
		"linux/arm64/i-tracker=" + filepath.Join("_release", "linux-arm64", "i-tracker"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("discover() = %q, want %q", got, want)
		}
	}
}
