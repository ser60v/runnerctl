//go:build !windows

package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnixCredentialPermissionsAndSymlinks(t *testing.T) {
	setup(t)
	if err := saveCredential("default", credential{"https://one.example", testToken}); err != nil {
		t.Fatal(err)
	}
	path, _ := credentialPath("default")
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential is not private")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredential("default"); err == nil {
		t.Fatal("world-readable credential accepted")
	}
	_ = os.Remove(path)
	target := filepath.Join(t.TempDir(), "outside")
	_ = os.WriteFile(target, []byte(`{}`), 0600)
	_ = os.Symlink(target, path)
	if _, err := loadCredential("default"); err == nil {
		t.Fatal("credential symlink accepted")
	}
}
