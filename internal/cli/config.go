package cli

import (
	"bytes"
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
	return filepath.Join(dir, name+credentialExtension), nil
}

func loadCredential(name string) (credential, error) {
	path, err := credentialPath(name)
	if err != nil {
		return credential{}, err
	}
	data, err := readCredentialFile(path)
	if err != nil {
		return credential{}, err
	}
	if len(data) > 8192 {
		return credential{}, errors.New("credential file exceeds size limit")
	}
	var c credential
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return credential{}, errors.New("invalid credential file")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || !tokenPattern.MatchString(c.Token) {
		return credential{}, errors.New("invalid credential file")
	}
	c.Server, err = canonicalServer(c.Server)
	if err != nil {
		return credential{}, err
	}
	return c, nil
}

func saveCredential(name string, c credential) error {
	path, err := credentialPath(name)
	if err != nil {
		return err
	}
	if !tokenPattern.MatchString(c.Token) {
		return errors.New("invalid personal token")
	}
	c.Server, err = canonicalServer(c.Server)
	if err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return writeCredentialFile(path, data)
}

func validateUUID(name, value string) error {
	if !uuidPattern.MatchString(value) || value == "00000000-0000-0000-0000-000000000000" {
		return fmt.Errorf("%s must be a lowercase, nonzero UUID", name)
	}
	return nil
}
