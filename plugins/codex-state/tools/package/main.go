// Package builds standalone, hash-verified Linux plugin archives. Signing keys
// are read only from an explicit local file and are never included in artifacts.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/baiyucraft/codex-state-plugin/core"
	pluginv1 "github.com/baiyucraft/codex-state-plugin/sdk/v1"
)

func main() {
	output := flag.String("out", "dist", "artifact directory")
	keyFile := flag.String("signing-key", "", "Ed25519 PKCS8 PEM file (optional; never bundled)")
	keyID := flag.String("key-id", "", "trusted publisher key ID")
	arches := flag.String("arches", "amd64,arm64", "Linux architectures")
	flag.Parse()
	if err := run(*output, *keyFile, *keyID, strings.Split(*arches, ",")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(out, keyFile, keyID string, arches []string) error {
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	var key ed25519.PrivateKey
	if keyFile != "" {
		if strings.TrimSpace(keyID) == "" {
			return errors.New("key-id required for signing")
		}
		raw, err := os.ReadFile(keyFile)
		if err != nil {
			return errors.New("signing key unavailable")
		}
		block, _ := pem.Decode(raw)
		if block == nil {
			return errors.New("signing key must be PKCS8 PEM")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return errors.New("invalid PKCS8 signing key")
		}
		var ok bool
		key, ok = parsed.(ed25519.PrivateKey)
		if !ok {
			return errors.New("signing key must be Ed25519")
		}
	}
	var sums strings.Builder
	seen := map[string]bool{}
	for _, arch := range arches {
		if (arch != "amd64" && arch != "arm64") || seen[arch] {
			return errors.New("architectures must be unique amd64/arm64 values")
		}
		seen[arch] = true
		binary := filepath.Join(out, "codex-state-linux-"+arch)
		cmd := exec.Command("go", "build", "-p", "2", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", binary, "./cmd/codex-state")
		cmd.Env = buildEnv(arch)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build linux/%s failed: %w", arch, err)
		}
		files := map[string][]byte{}
		data, err := os.ReadFile(binary)
		if err != nil {
			return err
		}
		binaryPath := "bin/codex-state-linux-" + arch
		files[binaryPath] = data
		if err := filepath.WalkDir("ui/dist", func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("UI symlinks are not allowed")
			}
			rel, err := filepath.Rel("ui/dist", path)
			if err != nil {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files["ui/"+filepath.ToSlash(rel)] = content
			return nil
		}); err != nil {
			return fmt.Errorf("build plugin UI before packaging: %w", err)
		}
		for _, name := range []string{"LICENSE", "THIRD_PARTY_NOTICES.md", "sources.lock.json", "MAINTENANCE.md"} {
			content, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			files[name] = content
		}
		if len(files["ui/index.html"]) == 0 {
			return errors.New("missing UI entrypoint")
		}
		hashes := map[string]string{}
		for name, content := range files {
			hashes[name] = digest(content)
		}
		manifest := map[string]any{
			"schema_version": 1, "id": "baiyu.codex-state", "name": "Codex STATE", "version": core.Version, "author": "Baiyu fork contributors",
			"description":    "Account/model scoped Codex STATE lifecycle and transport",
			"requires":       map[string]any{"sub2api": ">=0.2.7-baiyu", "recommended_sub2api_version": "0.2.7-baiyu", "tested_sub2api_versions": []string{"0.2.7-baiyu"}, "plugin_protocol": 1, "transport_api": 1, "ui_bridge": 1, "host_service_api": 2, "host_features": pluginv1.HostFeatures},
			"capabilities":   []map[string]string{{"id": "openai.oauth.outbound_transport.v1", "platform": "openai", "account_type": "oauth"}},
			"config_secrets": []string{"harvest_proxy_url", "dial_proxy_url"},
			"runtimes":       map[string]any{"linux-" + arch: map[string]string{"path": binaryPath}}, "ui": map[string]string{"entrypoint": "ui/index.html"}, "files": hashes,
		}
		raw, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		files["manifest.json"] = raw
		if key != nil {
			signature, _ := json.Marshal(map[string]string{"algorithm": "ed25519", "key_id": keyID, "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(key, raw))})
			files["signature.json"] = signature
		}
		archive, err := archiveFiles(files)
		if err != nil {
			return err
		}
		name := fmt.Sprintf("baiyu.codex-state-%s-linux-%s.s2plugin", core.Version, arch)
		if err := os.WriteFile(filepath.Join(out, name), archive, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%s  %s\n", digest(archive), name)
		fmt.Printf("package=%s signed=%t\n", filepath.Join(out, name), key != nil)
	}
	return os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(sums.String()), 0o600)
}

func buildEnv(arch string) []string {
	env := []string{}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		switch strings.ToUpper(key) {
		case "GOOS", "GOARCH", "CGO_ENABLED":
			continue
		}
		env = append(env, value)
	}
	return append(env, "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func archiveFiles(files map[string][]byte) ([]byte, error) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "\\") {
			return nil, errors.New("unsafe archive member")
		}
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetModTime(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
		header.SetMode(0o600)
		if strings.HasPrefix(name, "bin/") {
			header.SetMode(0o700)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err = entry.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
