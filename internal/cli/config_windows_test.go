package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsCredentialEncryptionAndContextBinding(t *testing.T) {
	setup(t)
	want := credential{"https://one.example", testToken}
	if err := saveCredential("default", want); err != nil {
		t.Fatal(err)
	}
	path, _ := credentialPath("default")
	data, err := os.ReadFile(path)
	if err != nil || bytes.Contains(data, []byte(want.Token)) || bytes.Contains(data, []byte(want.Server)) {
		t.Fatal("credential or origin stored without encryption", err)
	}
	if got, err := loadCredential("default"); err != nil || got != want {
		t.Fatal("DPAPI round trip failed", err)
	}
	if err := saveCredential("other", want); err != nil {
		t.Fatal(err)
	}
	other, _ := credentialPath("other")
	if err := os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredential("other"); err == nil {
		t.Fatal("encrypted credential could be moved into another context")
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredential("default"); err == nil {
		t.Fatal("modified DPAPI credential accepted")
	}
	if err := saveCredential("default", want); err != nil {
		t.Fatal("atomic credential replacement failed", err)
	}
	if got, err := loadCredential("default"); err != nil || got != want {
		t.Fatal("replacement could not be decrypted", err)
	}
	if err := removeCredentialFile(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("logout did not remove credential")
	}
}

func TestWindowsCredentialRejectsBroadACLAndHardlinks(t *testing.T) {
	for _, target := range []string{"directory", "file", "hardlink"} {
		t.Run(target, func(t *testing.T) {
			setup(t)
			want := credential{"https://one.example", testToken}
			if err := saveCredential("default", want); err != nil {
				t.Fatal(err)
			}
			path, _ := credentialPath("default")
			if target == "hardlink" {
				if err := os.Link(path, filepath.Join(filepath.Dir(path), "linked-credential")); err != nil {
					t.Fatal(err)
				}
			} else {
				_, sid, err := credentialSecurity(false)
				if err != nil {
					t.Fatal(err)
				}
				name, inheritance := path, ""
				if target == "directory" {
					name, inheritance = filepath.Dir(path), "OICI"
				}
				sd, err := windows.SecurityDescriptorFromString("D:P(A;" + inheritance + ";FA;;;" + sid.String() + ")(A;" + inheritance + ";FR;;;WD)")
				if err != nil {
					t.Fatal(err)
				}
				dacl, _, err := sd.DACL()
				if err != nil {
					t.Fatal(err)
				}
				if err := windows.SetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadCredential("default"); err == nil {
				t.Fatal("unsafe credential destination was read")
			}
			if err := saveCredential("default", want); err == nil {
				t.Fatal("unsafe credential destination was overwritten")
			}
			if err := removeCredentialFile(path); err == nil {
				t.Fatal("unsafe credential destination was used for logout")
			}
		})
	}
}

func TestWindowsCredentialRejectsJunctionAncestors(t *testing.T) {
	setup(t)
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "junction")
	// mklink /J is available without symlink privileges. All arguments are
	// synthetic test paths, quoted for cmd.exe rather than user input.
	command := exec.Command("cmd.exe")
	command.Dir = root
	command.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /c mklink /J "` + link + `" "` + target + `"`}
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create test junction: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	t.Setenv("RUNNERCTL_CONFIG_DIR", filepath.Join(link, "private-config"))
	if err := saveCredential("default", credential{"https://one.example", testToken}); err == nil {
		t.Fatal("junction ancestor accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "private-config")); !os.IsNotExist(err) {
		t.Fatal("credential directory created through a junction")
	}
}

func TestWindowsCredentialRejectsPlaintextAndRemotePaths(t *testing.T) {
	setup(t)
	want := credential{"https://one.example", testToken}
	if err := saveCredential("default", want); err != nil {
		t.Fatal(err)
	}
	path, _ := credentialPath("default")
	if err := os.WriteFile(path, []byte(`{"server":"https://one.example","token":"`+testToken+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredential("default"); err == nil {
		t.Fatal("plaintext Windows credential accepted")
	}
	for _, dir := range []string{`\\server\share\runnerctl`, `\\?\C:\runnerctl`, `C:\runnerctl:stream`, `C:\runnerctl.`, `C:\runnerctl `} {
		if unlock, _, err := lockWindowsCredentialDirectory(dir, true); err == nil {
			unlock()
			t.Fatalf("unsafe Windows path accepted: %q", dir)
		}
	}
}
