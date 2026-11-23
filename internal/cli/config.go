package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const defaultServer = "https://runners.retardsoftware.com"

var contextName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var tokenPattern = regexp.MustCompile(`^rp_pat_[A-Za-z0-9_-]{43}$`)
var sitePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type credential struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func canonicalServer(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("server must be an HTTPS origin without credentials, path or query")
	}
	ip := net.ParseIP(u.Hostname())
	loopback := strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return "", errors.New("HTTPS is required (HTTP is allowed only on loopback)")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

func configDir() (string, error) {
	if dir := os.Getenv("RUNNERCTL_CONFIG_DIR"); dir != "" {
		return filepath.Abs(dir)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runnerctl"), nil
}

func credentialPath(name string) (string, error) {
	if !contextName.MatchString(name) {
		return "", errors.New("invalid context name")
	}
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

func loadCredential(name string) (credential, error) {
	path, err := credentialPath(name)
	if err != nil {
		return credential{}, err
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return credential{}, err
	}
	if !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return credential{}, errors.New("config directory must be private (0700)")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return credential{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return credential{}, errors.New("credential file must be a regular private file (0600)")
	}
	f, err := os.Open(path)
	if err != nil {
		return credential{}, err
	}
	defer f.Close()
	var c credential
	dec := json.NewDecoder(io.LimitReader(f, 8193))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, errors.New("invalid credential file")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || !tokenPattern.MatchString(c.Token) {
		return c, errors.New("invalid credential file")
	}
	c.Server, err = canonicalServer(c.Server)
	return c, err
}

func saveCredential(name string, c credential) error {
	path, err := credentialPath(name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("config directory must be private (0700)")
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return nil
}

func validateUUID(name, value string) error {
	if !uuidPattern.MatchString(value) || value == "00000000-0000-0000-0000-000000000000" {
		return fmt.Errorf("%s must be a lowercase, nonzero UUID", name)
	}
	return nil
}
