package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestArchiveIsDeterministicAndPreservesExecutableMode(t *testing.T) {
	files := map[string][]byte{"ui/index.html": []byte("<main>STATE</main>"), "bin/plugin": []byte("binary"), "manifest.json": []byte(`{"id":"baiyu.codex-state"}`)}
	one, err := archiveFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	two, err := archiveFiles(files)
	if err != nil || !bytes.Equal(one, two) {
		t.Fatal("archive is not deterministic", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(one), int64(len(one)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != len(files) {
		t.Fatal("missing archive entries")
	}
	for _, file := range reader.File {
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		_ = stream.Close()
		if err != nil || !bytes.Equal(files[file.Name], data) {
			t.Fatal("content mismatch", file.Name, err)
		}
		if strings.HasPrefix(file.Name, "bin/") && file.Mode().Perm() != 0700 {
			t.Fatal("binary not executable")
		}
	}
}

func TestArchiveRejectsUnsafeMembers(t *testing.T) {
	for _, name := range []string{"../secret", "/absolute", "ui/../../secret", `ui\escape`} {
		if _, err := archiveFiles(map[string][]byte{name: []byte("x")}); err == nil {
			t.Errorf("accepted %q", name)
		}
	}
}

func TestBuildEnvOverridesInheritedTarget(t *testing.T) {
	t.Setenv("GOOS", "windows")
	t.Setenv("GOARCH", "386")
	t.Setenv("CGO_ENABLED", "1")
	values := map[string][]string{}
	for _, entry := range buildEnv("arm64") {
		key, value, _ := strings.Cut(entry, "=")
		values[strings.ToUpper(key)] = append(values[strings.ToUpper(key)], value)
	}
	for key, wanted := range map[string]string{"GOOS": "linux", "GOARCH": "arm64", "CGO_ENABLED": "0"} {
		if len(values[key]) != 1 || values[key][0] != wanted {
			t.Fatalf("%s: %v", key, values[key])
		}
	}
}

func TestNativeManifestIncludesDefinitionAndCapability(t *testing.T) {
	hashes := map[string]string{
		"bin/codex-state-linux-amd64": "binary-sha",
		"ui/admin-ui.json":            "admin-ui-sha",
		"ui/index.html":               "iframe-fallback-sha",
	}
	manifest := buildManifest("amd64", "bin/codex-state-linux-amd64", hashes)
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		SchemaVersion int `json:"schema_version"`
		Requires      struct {
			UIBridge int `json:"ui_bridge"`
			AdminUI  int `json:"admin_ui"`
		} `json:"requires"`
		UI struct {
			Type       string `json:"type"`
			Definition string `json:"definition"`
		} `json:"ui"`
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SchemaVersion != 2 || decoded.Requires.AdminUI != 1 || decoded.Requires.UIBridge != 0 {
		t.Fatalf("unexpected native requirements: %+v", decoded)
	}
	if decoded.UI.Type != "native" || decoded.UI.Definition != "ui/admin-ui.json" {
		t.Fatalf("unexpected native UI declaration: %+v", decoded.UI)
	}
	if decoded.Files[decoded.UI.Definition] != "admin-ui-sha" || decoded.Files["ui/index.html"] != "iframe-fallback-sha" {
		t.Fatalf("native definition or iframe fallback missing from signed files: %#v", decoded.Files)
	}
}
