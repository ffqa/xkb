package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func TestParseChecksum(t *testing.T) {
	sums := "abc123  xkb-v0.1.0-darwin-arm64.tar.gz\ndef456  other.tar.gz\n"
	if got := parseChecksum(sums, "xkb-v0.1.0-darwin-arm64.tar.gz"); got != "abc123" {
		t.Fatalf("got %q", got)
	}
	if got := parseChecksum(sums, "missing"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractFromTarGz(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "xkb.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho hi\n")
	if err := tw.WriteHeader(&tar.Header{Name: "xkb-v0.1.0/xkb", Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	f.Close()

	out := filepath.Join(dir, "out")
	of, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := extractFromTarGz(archive, of); err != nil {
		of.Close()
		t.Fatal(err)
	}
	of.Close()
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("content %q", got)
	}
}

func TestAutoUpdateEligible(t *testing.T) {
	home, _ := os.UserHomeDir()
	if !autoUpdateEligible(filepath.Join(home, ".local", "bin", "xkb")) {
		t.Fatal("local bin should be eligible")
	}
	if !autoUpdateEligible("/opt/homebrew/Cellar/xkb/0.1.0/bin/xkb") {
		t.Fatal("brew cellar should be eligible")
	}
	if autoUpdateEligible("/Users/gouki/server/wwwroot/xpier/xkb/bin/xkb") {
		t.Fatal("repo build must not auto-update")
	}
}
