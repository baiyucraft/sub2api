package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (string, string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	publicPath := filepath.Join(directory, "public.pem")
	evidencePath := filepath.Join(directory, "evidence.json")
	signaturePath := filepath.Join(directory, "evidence.sig")
	evidence := []byte("{\"schema\":1}\n")
	if err := os.WriteFile(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signaturePath, ed25519.Sign(privateKey, evidence), 0o600); err != nil {
		t.Fatal(err)
	}
	return publicPath, evidencePath, signaturePath
}

func TestVerifyValidSignature(t *testing.T) {
	publicPath, evidencePath, signaturePath := fixture(t)
	if err := verify(publicPath, evidencePath, signaturePath); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsTamperedEvidence(t *testing.T) {
	publicPath, evidencePath, signaturePath := fixture(t)
	if err := os.WriteFile(evidencePath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verify(publicPath, evidencePath, signaturePath); err == nil {
		t.Fatal("tampered evidence was accepted")
	}
}

func TestVerifyRejectsTamperedSignature(t *testing.T) {
	publicPath, evidencePath, signaturePath := fixture(t)
	if err := os.WriteFile(signaturePath, make([]byte, ed25519.SignatureSize), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verify(publicPath, evidencePath, signaturePath); err == nil {
		t.Fatal("tampered signature was accepted")
	}
}

func TestVerifyRejectsOversizedEvidence(t *testing.T) {
	publicPath, evidencePath, signaturePath := fixture(t)
	if err := os.WriteFile(evidencePath, make([]byte, maxEvidenceSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verify(publicPath, evidencePath, signaturePath); err == nil {
		t.Fatal("oversized evidence was accepted")
	}
}

func TestVerifyRejectsSymlink(t *testing.T) {
	publicPath, evidencePath, signaturePath := fixture(t)
	link := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.Symlink(evidencePath, link); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	if err := verify(publicPath, link, signaturePath); err == nil {
		t.Fatal("symlink evidence was accepted")
	}
}
