package cli

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const credentialExtension = ".dpapi"
const windowsCredentialHeader = "runnerctl-dpapi-v1\x00"
const windowsFileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

func credentialSecurity(directory bool) (*windows.SECURITY_DESCRIPTOR, *windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	// Protected DACL: only this user receives access; no inherited grants.
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;" + inherit + ";FA;;;" + user.User.Sid.String() + ")")
	return sd, user.User.Sid, err
}

func openWindowsCredentialPath(path string, access, disposition uint32, sd *windows.SECURITY_DESCRIPTOR) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	var attributes *windows.SecurityAttributes
	if sd != nil {
		attributes = &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	}
	h, err := windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, attributes, disposition, windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return windows.InvalidHandle, &os.PathError{Op: "open credential path", Path: path, Err: err}
	}
	return h, nil
}

func checkWindowsCredentialHandle(h windows.Handle, directory, private bool, sid *windows.SID) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory || (!directory && info.NumberOfLinks != 1) {
		return errors.New("credential paths must not contain reparse points, links or alternate file types")
	}
	if !private {
		return nil
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !owner.Equals(sid) {
		return errors.New("credential path must be owned by the current Windows user")
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("credential path must have a protected private Windows DACL")
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil || dacl.AceCount != 1 {
		return errors.New("credential path must allow only the current Windows user")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 0, &ace); err != nil {
		return err
	}
	allowedFlags := byte(0)
	if directory {
		allowedFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != allowedFlags || ace.Mask != windowsFileAllAccess || !(*windows.SID)(unsafe.Pointer(&ace.SidStart)).Equals(sid) {
		return errors.New("credential path has an unexpected Windows access grant")
	}
	return nil
}

// Hold every ancestor without FILE_SHARE_DELETE until the operation finishes.
// This prevents directory/junction substitution after the no-reparse checks.
func lockWindowsCredentialDirectory(dir string, create bool) (func(), *windows.SID, error) {
	volume := filepath.VolumeName(dir)
	if len(volume) != 2 || volume[1] != ':' || !filepath.IsAbs(dir) {
		return nil, nil, errors.New("credentials require an absolute local Windows drive path")
	}
	parts := strings.Split(strings.TrimPrefix(dir[len(volume):], `\`), `\`)
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, ":/") || strings.TrimRight(part, ". ") != part {
			return nil, nil, errors.New("invalid Windows credential directory")
		}
	}
	sd, sid, err := credentialSecurity(true)
	if err != nil {
		return nil, nil, err
	}
	var handles []windows.Handle
	closeAll := func() {
		for i := len(handles) - 1; i >= 0; i-- {
			_ = windows.CloseHandle(handles[i])
		}
	}
	current := volume + `\`
	for i := -1; i < len(parts); i++ {
		if i >= 0 {
			current = filepath.Join(current, parts[i])
		}
		h, openErr := openWindowsCredentialPath(current, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, nil)
		if create && os.IsNotExist(openErr) && i >= 0 {
			p, _ := windows.UTF16PtrFromString(current)
			attributes := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
			if err = windows.CreateDirectory(p, attributes); err == nil || errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
				h, openErr = openWindowsCredentialPath(current, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, nil)
			} else {
				openErr = err
			}
		}
		if openErr != nil {
			closeAll()
			return nil, nil, openErr
		}
		handles = append(handles, h)
		if err = checkWindowsCredentialHandle(h, true, i == len(parts)-1, sid); err != nil {
			closeAll()
			return nil, nil, err
		}
	}
	return closeAll, sid, nil
}

func credentialBlob(data []byte) windows.DataBlob {
	return windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}

func protectWindowsCredential(path string, data []byte, decrypt bool) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("invalid protected credential")
	}
	input := credentialBlob(data)
	entropyBytes := []byte("runnerctl/current-user/credential/v1/" + filepath.Base(path))
	entropy := credentialBlob(entropyBytes)
	var output windows.DataBlob
	var err error
	if decrypt {
		err = windows.CryptUnprotectData(&input, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	} else {
		// Deliberately omit CRYPTPROTECT_LOCAL_MACHINE: other users cannot decrypt.
		err = windows.CryptProtectData(&input, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	}
	if err != nil {
		return nil, errors.New("Windows could not protect or unlock this user's credential")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(output.Data)))
	result := append([]byte(nil), unsafe.Slice(output.Data, output.Size)...)
	clear(unsafe.Slice(output.Data, output.Size))
	return result, nil
}

func readCredentialFile(path string) ([]byte, error) {
	unlock, sid, err := lockWindowsCredentialDirectory(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	h, err := openWindowsCredentialPath(path, windows.GENERIC_READ|windows.READ_CONTROL, windows.OPEN_EXISTING, nil)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	if err := checkWindowsCredentialHandle(h, false, true, sid); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, 16385))
	if err != nil {
		return nil, err
	}
	if len(data) > 16384 || !bytes.HasPrefix(data, []byte(windowsCredentialHeader)) {
		return nil, errors.New("invalid protected credential file")
	}
	return protectWindowsCredential(path, data[len(windowsCredentialHeader):], true)
}

func writeCredentialFile(path string, data []byte) error {
	unlock, sid, err := lockWindowsCredentialDirectory(filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer unlock()
	if h, err := openWindowsCredentialPath(path, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, nil); err == nil {
		check := checkWindowsCredentialHandle(h, false, true, sid)
		_ = windows.CloseHandle(h)
		if check != nil {
			return check
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	protected, err := protectWindowsCredential(path, data, false)
	if err != nil {
		return err
	}
	sd, _, err := credentialSecurity(false)
	if err != nil {
		return err
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), ".credential-"+hex.EncodeToString(random))
	h, err := openWindowsCredentialPath(temporary, windows.GENERIC_WRITE|windows.READ_CONTROL, windows.CREATE_NEW, sd)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), temporary)
	defer os.Remove(temporary)
	defer f.Close()
	if err := checkWindowsCredentialHandle(h, false, true, sid); err != nil {
		return err
	}
	if _, err := f.Write(append([]byte(windowsCredentialHeader), protected...)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	from, _ := windows.UTF16PtrFromString(temporary)
	to, _ := windows.UTF16PtrFromString(path)
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

func removeCredentialFile(path string) error {
	unlock, sid, err := lockWindowsCredentialDirectory(filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer unlock()
	h, err := openWindowsCredentialPath(path, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.OPEN_EXISTING, nil)
	if err != nil {
		return err
	}
	err = checkWindowsCredentialHandle(h, false, true, sid)
	_ = windows.CloseHandle(h)
	if err != nil {
		return err
	}
	return os.Remove(path)
}
