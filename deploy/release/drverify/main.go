package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	maxPublicKeySize = 64 * 1024
	maxEvidenceSize  = 1024 * 1024
)

func readRegularFile(path string, maxSize int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("input is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > maxSize {
		return nil, errors.New("input size is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, errors.New("input changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || int64(len(data)) > maxSize {
		return nil, errors.New("input size is invalid")
	}
	return data, nil
}

func verify(publicKeyPath, evidencePath, signaturePath string) error {
	publicPEM, err := readRegularFile(publicKeyPath, maxPublicKeySize)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	block, rest := pem.Decode(publicPEM)
	if block == nil || len(rest) != 0 || block.Type != "PUBLIC KEY" {
		return errors.New("public key PEM is invalid")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return errors.New("public key is invalid")
	}
	publicKey, ok := parsed.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("public key is not Ed25519")
	}
	evidence, err := readRegularFile(evidencePath, maxEvidenceSize)
	if err != nil {
		return fmt.Errorf("read evidence: %w", err)
	}
	signature, err := readRegularFile(signaturePath, ed25519.SignatureSize)
	if err != nil {
		return fmt.Errorf("read signature: %w", err)
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, evidence, signature) {
		return errors.New("signature verification failed")
	}
	return nil
}

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: sub2api-verify-dr-evidence <public-key> <evidence> <signature>")
		os.Exit(2)
	}
	if err := verify(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, "DR evidence verification failed")
		os.Exit(1)
	}
	fmt.Println("signature_status=verified")
}
