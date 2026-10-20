package bootstrap

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// UvVersion is the pinned uv release used when the host has no uv. Pinned,
// checksum-verified download from the astral-sh/uv GitHub releases — never
// `curl | sh` (the harness's own unpinned-install rule, applied here too).
const UvVersion = "0.13.4"

// EnsureUv returns a working uv binary path, bootstrapping the pinned release
// into the CLI bin directory when the host has none.
func EnsureUv(binDir string, log func(format string, args ...any)) (string, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	if p, err := lookPath("uv"); err == nil {
		return p, nil
	}
	dest := filepath.Join(binDir, "uv")
	if fi, err := os.Stat(dest); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
		return dest, nil
	}
	log("uv not found — bootstrapping pinned release %s into %s", UvVersion, binDir)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", fmt.Errorf("create bin dir: %w", err)
	}
	if err := downloadUv(dest); err != nil {
		return "", err
	}
	log("uv %s installed", UvVersion)
	return dest, nil
}

func lookPath(name string) (string, error) {
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}

// uvAssetName maps the host platform onto the uv release asset name.
func uvAssetName() (string, error) {
	arch := runtime.GOARCH
	switch runtime.GOOS {
	case "linux":
		switch arch {
		case "amd64":
			return "uv-x86_64-unknown-linux-gnu.tar.gz", nil
		case "arm64":
			return "uv-aarch64-unknown-linux-gnu.tar.gz", nil
		}
	case "darwin":
		switch arch {
		case "amd64":
			return "uv-x86_64-apple-darwin.tar.gz", nil
		case "arm64":
			return "uv-aarch64-apple-darwin.tar.gz", nil
		}
	}
	return "", fmt.Errorf("unsupported platform %s/%s for the uv bootstrap", runtime.GOOS, arch)
}

func downloadUv(dest string) error {
	asset, err := uvAssetName()
	if err != nil {
		return err
	}
	base := fmt.Sprintf("https://github.com/astral-sh/uv/releases/download/%s", UvVersion)
	tarURL := base + "/" + asset
	shaURL := tarURL + ".sha256"

	wantSHA, err := fetchString(shaURL)
	if err != nil {
		return fmt.Errorf("fetch uv checksum (release %s publishes %s.sha256): %w", UvVersion, asset, err)
	}
	wantSHA = strings.TrimSpace(strings.Fields(wantSHA)[0])

	tarball, err := fetchBytes(tarURL)
	if err != nil {
		return fmt.Errorf("download uv %s: %w", UvVersion, err)
	}
	sum := sha256.Sum256(tarball)
	gotSHA := hex.EncodeToString(sum[:])
	if !strings.EqualFold(gotSHA, wantSHA) {
		return fmt.Errorf("uv checksum mismatch: want %s, got %s", wantSHA, gotSHA)
	}

	gz, err := gzip.NewReader(strings.NewReader(string(tarball)))
	if err != nil {
		return fmt.Errorf("decompress uv tarball: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read uv tarball: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "uv" {
			continue
		}
		out, err := os.OpenFile(dest+".tmp", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("write uv binary: %w", err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return fmt.Errorf("write uv binary: %w", err)
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Rename(dest+".tmp", dest)
	}
	return fmt.Errorf("uv binary not found in release tarball")
}

func fetchString(url string) (string, error) {
	body, err := fetchBytes(url)
	if err != nil {
		return "", err
	}
	if len(body) > 4096 {
		return "", fmt.Errorf("unexpectedly large response")
	}
	return string(body), nil
}

func fetchBytes(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 512<<20))
}
