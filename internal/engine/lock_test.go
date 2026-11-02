package engine

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAcquireLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.lock")
	first, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second acquire: %v", err)
	}
	holder := ReadLockHolder(path)
	if holder == "" {
		t.Fatal("expected lock holder info, got empty")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	second.Close()
}
