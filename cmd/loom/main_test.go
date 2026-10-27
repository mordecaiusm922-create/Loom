package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInitConfigIsReadableOnlyByItsOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits do not apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "loom.config.json")
	if err := writeDefaultConfig(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Fatalf("mode = %o, want no group or other permissions", mode)
	}
}

func TestInitNeverOverwritesAnExistingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loom.config.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDefaultConfig(path); !os.IsExist(err) {
		t.Fatalf("err = %v, want an already-exists error", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "{}" {
		t.Fatalf("existing config was modified: %q", data)
	}
}
