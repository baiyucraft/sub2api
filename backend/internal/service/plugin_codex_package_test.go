package service

import (
	"archive/zip"
	"bytes"
	"context"
	"debug/elf"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPluginCodexStateBuiltArchivesMatchHostContract(t *testing.T) {
	paths := os.Getenv("SUB2API_TEST_CODEX_STATE_PACKAGES")
	if paths == "" {
		t.Skip("independent Linux packages not supplied")
	}
	for _, path := range filepath.SplitList(paths) {
		t.Run(path, func(t *testing.T) {
			archive, err := zip.OpenReader(path)
			require.NoError(t, err)
			defer archive.Close()
			installer := NewPluginPackageInstaller(testPluginConfig(t.TempDir(), true), PluginHostInfo{Version: "0.2.7-baiyu"})
			var target string
			for _, arch := range []string{"amd64", "arm64"} {
				if strings.HasSuffix(filepath.Base(path), "-linux-"+arch+".s2plugin") {
					target = "linux-" + arch
				}
			}
			require.NotEmpty(t, target, "expected a Linux release package")
			manifest, _, signature, err := installer.inspectArchiveForRuntime(&archive.Reader, target)
			require.NoError(t, err)
			require.Len(t, manifest.Runtimes, 1)
			require.Contains(t, manifest.Runtimes, target)
			if target != manifest.RuntimeKey() {
				_, _, _, err := installer.inspectArchive(&archive.Reader)
				require.ErrorContains(t, err, manifest.RuntimeKey(), "production inspection must still reject a foreign platform")
			}
			extracted := t.TempDir()
			require.NoError(t, installer.extractArchive(context.Background(), &archive.Reader, manifest, extracted))
			binaryBytes, err := os.ReadFile(filepath.Join(extracted, filepath.FromSlash(manifest.Runtimes[target].Path)))
			require.NoError(t, err)
			binary, err := elf.NewFile(bytes.NewReader(binaryBytes))
			require.NoError(t, err)
			defer binary.Close()
			require.Equal(t, elf.ELFCLASS64, binary.Class)
			wantMachine := elf.EM_X86_64
			if target == "linux-arm64" {
				wantMachine = elf.EM_AARCH64
			}
			require.Equal(t, wantMachine, binary.Machine)
			require.Equal(t, "baiyu.codex-state", manifest.ID)
			require.Equal(t, "0.1.0", manifest.Version)
			require.Contains(t, []string{PluginSignatureUnsigned, PluginSignatureTrusted}, signature)
			require.ElementsMatch(t, []string{"harvest_proxy_url", "dial_proxy_url"}, manifest.ConfigSecrets)
			for _, feature := range []string{"admission.v1", "state-cas.v1", "leases.v1", "oauth-like.v1", "request-completion.v1", "config-secrets.v1"} {
				require.True(t, pluginRequiresFeature(manifest, feature))
			}
			for _, file := range []string{"ui/index.html", "ui/app.js", "ui/style.css", "LICENSE", "THIRD_PARTY_NOTICES.md", "sources.lock.json", "MAINTENANCE.md"} {
				require.Contains(t, manifest.Files, file)
			}
		})
	}
}
