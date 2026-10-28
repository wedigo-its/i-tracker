// Command i-tracker-feed builds the signed update feed for a release: it
// copies the binaries into <version>/<os>-<arch>/, writes latest.json with
// their SHA-256 digests and signs it with the wedigo release key into
// latest.json.sig. The output folder is uploaded as is to
// https://www.wedigo.net/update/i-tracker/.
//
// i-Tracker has one channel for all versions (selfupdate.ChannelURL): every
// release, 0.x as well as later major versions, goes to the feed root. Unlike
// spherifyer-feed and acmefyer-feed there is therefore no vN/ folder, no
// "major" field and no "announce" command.
//
// Vendor-only tool; it is not shipped. The private key never enters the
// repository: pass it with -key, WEDIGO_RELEASE_KEY (base64) or
// WEDIGO_RELEASE_KEY_FILE.
//
// Usage:
//
//	go run ./cmd/i-tracker-feed -version v0.0.1 [-notes "..."] [-out dir] [-bin os/arch/program=path ...]
//
// -version is the version exactly as the binary reports it ("i-tracker
// -version"); build.cmd takes it from "git describe", so a tag v0.0.1 gives
// "v0.0.1". Without -bin the binaries of "build.cmd release linux" are used:
// _release\i-tracker.exe, _release/linux-amd64/i-tracker and
// _release/linux-arm64/i-tracker, as far as present.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"i-tracker/internal/selfupdate"
)

var programs = []string{"i-tracker"}

// platforms are the targets of build.cmd, as os/arch.
var platforms = []string{"windows/amd64", "linux/amd64", "linux/arm64"}

// defaultOut is the feed root the output goes to without -out.
var defaultOut = filepath.Join("_release", "update", "i-tracker")

type binFlags []string

func (b *binFlags) String() string     { return strings.Join(*b, ", ") }
func (b *binFlags) Set(v string) error { *b = append(*b, v); return nil }

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("i-tracker-feed", flag.ExitOnError)
	version := fs.String("version", "", "release version, as the binaries report it (required, e.g. v0.0.1)")
	notes := fs.String("notes", "", "short release notes shown in the update card, or an https URL")
	out := fs.String("out", defaultOut, "output folder (the feed root)")
	keyFile := fs.String("key", "", "file holding the base64 Ed25519 private key")
	var bins binFlags
	fs.Var(&bins, "bin", "binary as os/arch/program=path; repeatable")
	_ = fs.Parse(args)

	if !selfupdate.IsRelease(*version) {
		return fmt.Errorf("-version %q is no release version (expected e.g. v0.0.1)", *version)
	}
	priv, err := releaseKey(*keyFile)
	if err != nil {
		return err
	}
	if len(bins) == 0 {
		bins = discover()
	}
	if len(bins) == 0 {
		return errors.New("no binaries found; run build.cmd release linux, or pass -bin")
	}
	dir, err := build(buildOptions{version: *version, notes: *notes, out: *out, bins: bins}, priv)
	if err != nil {
		return err
	}
	fmt.Printf("feed for %s written to %s\n", *version, dir)
	fmt.Printf("upload the folder content to %s/ (version folder first, latest.json and latest.json.sig last; keep older version folders)\n",
		selfupdate.ChannelURL(selfupdate.DefaultFeedURL, *version))
	return nil
}

type buildOptions struct {
	version, notes string
	out            string // feed root
	bins           []string
}

// build copies the binaries below o.out, writes the manifest and signs it.
// It returns the folder holding latest.json, which is always the feed root.
func build(o buildOptions, priv ed25519.PrivateKey) (string, error) {
	if !selfupdate.IsRelease(o.version) {
		return "", fmt.Errorf("-version %q is no release version (expected e.g. v0.0.1)", o.version)
	}
	dir := o.out

	m := selfupdate.Manifest{Version: o.version, Notes: o.notes, Binaries: map[string]selfupdate.Binary{}}
	type staged struct {
		dst  string
		data []byte
	}
	var files []staged
	for _, spec := range o.bins {
		key, path, ok := strings.Cut(spec, "=")
		parts := strings.Split(key, "/")
		if !ok || len(parts) != 3 {
			return "", fmt.Errorf("-bin %q: expected os/arch/program=path", spec)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if err := checkVersion(path, parts[0], parts[1], data, o.version); err != nil {
			return "", err
		}
		rel := filepath.ToSlash(filepath.Join(o.version, parts[0]+"-"+parts[1], filepath.Base(path)))
		files = append(files, staged{filepath.Join(dir, filepath.FromSlash(rel)), data})
		sum := sha256.Sum256(data)
		m.Binaries[key] = selfupdate.Binary{URL: rel, SHA256: hex.EncodeToString(sum[:])}
		fmt.Printf("  %-28s %s\n", key, rel)
	}
	// Write only after every binary passed its check.
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f.dst), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(f.dst, f.data, 0o644); err != nil {
			return "", err
		}
	}
	return dir, writeSigned(dir, m, priv)
}

// writeSigned writes latest.json and latest.json.sig into dir.
func writeSigned(dir string, m selfupdate.Manifest, priv ed25519.PrivateKey) error {
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), manifest, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "latest.json.sig"), []byte(sig+"\n"), 0o644)
}

// checkVersion makes sure a binary reports the manifest version. A mismatch
// would make every client install the release, still see itself as outdated
// and stop updating. Binaries for this host are asked directly; for the
// others the version string has to be embedded.
func checkVersion(path, goos, goarch string, data []byte, version string) error {
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		out, err := exec.Command(path, "-version").Output()
		if err != nil {
			return fmt.Errorf("%s -version: %w", path, err)
		}
		for _, f := range strings.Fields(string(out)) {
			if f == version {
				return nil
			}
		}
		return fmt.Errorf("%s reports %q, not version %s; rebuild it from the release tag", path, strings.TrimSpace(string(out)), version)
	}
	if !bytes.Contains(data, []byte(version)) {
		return fmt.Errorf("%s does not contain version %s; rebuild it from the release tag", path, version)
	}
	return nil
}

// discover finds the binaries of build.cmd for every platform.
func discover() []string {
	var out []string
	for _, p := range programs {
		for _, pl := range platforms {
			goos, goarch, _ := strings.Cut(pl, "/")
			path := filepath.Join("_release", goos+"-"+goarch, p)
			if goos == "windows" {
				path = filepath.Join("_release", p+".exe")
			}
			if exists(path) {
				out = append(out, pl+"/"+p+"="+path)
			}
		}
	}
	sort.Strings(out)
	return out
}

func exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// releaseKey loads the signing key and makes sure it belongs to the release
// key embedded in the binaries.
func releaseKey(file string) (ed25519.PrivateKey, error) {
	priv, err := loadKey(file)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), selfupdate.ReleasePublicKey()) {
		return nil, errors.New("the private key does not belong to the release key embedded in the binaries; clients would reject the feed")
	}
	return priv, nil
}

func loadKey(file string) (ed25519.PrivateKey, error) {
	raw := os.Getenv("WEDIGO_RELEASE_KEY")
	if file == "" {
		file = os.Getenv("WEDIGO_RELEASE_KEY_FILE")
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		raw = string(data)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("no signing key: pass -key, or set WEDIGO_RELEASE_KEY or WEDIGO_RELEASE_KEY_FILE")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("decode signing key: %w", err)
	}
	switch len(key) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(key), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(key), nil
	}
	return nil, fmt.Errorf("signing key has %d bytes, want an Ed25519 key", len(key))
}
