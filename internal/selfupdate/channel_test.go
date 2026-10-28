// channel_test.go
// Tests for the single update channel: every version follows the feed root
// and updates across major versions
// Version: 2026.10.28

package selfupdate

import (
	"context"
	"encoding/json"
	"testing"
)

func TestChannelURLIsFeedRoot(t *testing.T) {
	const root = "https://www.wedigo.net/update/i-tracker"
	for _, current := range []string{"v0.0.1", "v1.2.3", "2.0.0", "v3.1.0-2-gabc123", "dev", ""} {
		for _, feed := range []string{root, root + "/"} {
			if got := ChannelURL(feed, current); got != root {
				t.Errorf("ChannelURL(%q, %q) = %q, want the feed root", feed, current, got)
			}
		}
	}
	u := New(DefaultFeedURL, ReleasePublicKey())
	for _, current := range []string{"v0.0.1", "v1.2.3"} {
		if got := u.ChannelURL(current); got != root {
			t.Errorf("Updater.ChannelURL(%q) = %q, want %q", current, got, root)
		}
	}
	if got := (*Updater)(nil).ChannelURL("v1.2.3"); got != "" {
		t.Errorf("nil updater ChannelURL = %q", got)
	}
}

func TestReleasePublicKeyEmbedded(t *testing.T) {
	if len(ReleasePublicKey()) == 0 {
		t.Fatal("the embedded release key does not decode")
	}
	if !New(DefaultFeedURL, ReleasePublicKey()).Enabled() {
		t.Fatal("updater with the built-in feed and key is not enabled")
	}
}

// Both a 0.x and a 1.x client read the manifest at the feed root, and a
// newer major version is an ordinary update.
func TestCheckAcrossMajorVersions(t *testing.T) {
	pub, priv := newKey(t)
	f := signedFeed(t, priv, Manifest{Version: "v1.2.3"})
	srv := f.serve(t)
	u := New(srv.URL, pub)
	for current, want := range map[string]bool{"v0.0.1": true, "v1.2.2": true, "v1.2.3": false, "v1.3.0": false} {
		rel, err := u.Check(context.Background(), current)
		if err != nil {
			t.Fatalf("Check(%s): %v", current, err)
		}
		if rel.Manifest.Version != "v1.2.3" || rel.Available != want {
			t.Errorf("Check(%s) = %+v, want release v1.2.3 available=%v", current, rel, want)
		}
	}
}

func TestManagerInstallsNewMajorVersion(t *testing.T) {
	pub, priv := newKey(t)
	exe := useExecutable(t, "old")
	payload := []byte("1.0 binary")
	f := signedFeed(t, priv, Manifest{Version: "v1.0.0", Binaries: map[string]Binary{platformKey(testProgram): {URL: "bin", SHA256: digest(payload)}}})
	f.binary = payload
	srv := f.serve(t)

	mgr := NewManager(Options{Updater: New(srv.URL, pub), Mode: ModeNotify, Program: testProgram, Current: "v0.0.1"})
	st, err := mgr.Install(context.Background())
	if err != nil || st.Installed != "v1.0.0" {
		t.Fatalf("Install = %+v, %v; want v1.0.0 installed", st, err)
	}
	if got := readFile(t, exe); got != string(payload) {
		t.Fatalf("executable = %q, want the 1.0 binary", got)
	}
}

// Manifests of the other products carry major and next_major; i-Tracker
// ignores both.
func TestManifestIgnoresChannelFields(t *testing.T) {
	var m Manifest
	if err := json.Unmarshal([]byte(`{"version":"v2.0.0","major":2,"next_major":{"version":"v3.0.0"},"binaries":{}}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != "v2.0.0" {
		t.Fatalf("manifest = %+v", m)
	}
}
