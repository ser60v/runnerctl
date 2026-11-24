package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

var knownScopes = []string{"catalog.read", "pools.read", "pools.write", "runs.read", "runs.cancel"}

func (a *App) ask(label string, secret bool) (string, error) {
	if a.prompt != nil {
		return a.prompt(label, secret)
	}
	f, ok := a.In.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return "", errors.New("interactive login needs a terminal; for automation use --token-stdin or RUNNERCTL_TOKEN")
	}
	fmt.Fprint(a.Err, label+": ")
	if secret {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(a.Err)
		return string(b), err
	}
	s, err := bufio.NewReader(f).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (a *App) auth(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage("auth requires login, status, logout, tokens or revoke")
	}
	switch args[0] {
	case "login":
		return a.login(ctx, args[1:])
	case "status":
		if len(args) != 1 {
			return usage("auth status takes no arguments")
		}
		c, err := a.client()
		if err != nil {
			return err
		}
		res, err := c.json(ctx, "GET", "/api/app/v1/cli/context", nil)
		if err != nil {
			return err
		}
		return a.output(res.Body)
	case "logout":
		if len(args) != 1 {
			return usage("auth logout takes no arguments")
		}
		path, err := credentialPath(a.context)
		if err != nil {
			return err
		}
		if err = removeCredentialFile(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return a.output(map[string]any{"context": a.context, "local_credentials_removed": true, "token_revoked": false, "note": "The server token remains valid until revoked or expired; RUNNERCTL_TOKEN is unaffected."})
	case "tokens", "revoke":
		if args[0] == "tokens" && len(args) != 1 {
			return usage("auth tokens takes no arguments")
		}
		if args[0] == "revoke" {
			if len(args) != 2 {
				return usage("auth revoke requires a token UUID")
			}
			if err := validateUUID("token", args[1]); err != nil {
				return usage(err.Error())
			}
		}
		c, _, err := a.sessionLogin(ctx, "")
		if err != nil {
			return err
		}
		defer endSession(c)
		path := "/api/auth/v1/me/personal-tokens"
		method := "GET"
		if args[0] == "revoke" {
			method = "DELETE"
			path += "/" + args[1]
		}
		res, err := c.json(ctx, method, path, nil)
		if err != nil {
			return err
		}
		return a.output(res.Body)
	default:
		return usage("unknown auth subcommand")
	}
}

func (a *App) authServer() (string, error) {
	if a.server != "" {
		return a.server, nil
	}
	c, err := loadCredential(a.context)
	if err == nil {
		return c.Server, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	return defaultServer, nil
}

func (a *App) login(ctx context.Context, args []string) error {
	f := a.flags("auth login")
	stdin := f.Bool("token-stdin", false, "read a token from stdin")
	org := f.String("organization", "", "organization UUID")
	projects := f.String("projects", "", "comma-separated project UUIDs")
	name := f.String("name", "runnerctl", "token label")
	scopes := f.String("scopes", strings.Join(knownScopes, ","), "comma-separated scopes")
	ttl := f.Duration("ttl", 7*24*time.Hour, "token lifetime")
	email := f.String("email", "", "account email")
	if err := f.Parse(args); err != nil {
		return usage(err.Error())
	}
	if len(f.Args()) > 0 {
		return usage("unexpected login arguments")
	}
	server, err := a.authServer()
	if err != nil {
		return err
	}
	if *stdin {
		data, err := io.ReadAll(io.LimitReader(a.In, 4097))
		if err != nil {
			return err
		}
		if len(data) > 4096 {
			return usage("token input exceeds limit")
		}
		token := strings.TrimSpace(string(data))
		if !tokenPattern.MatchString(token) {
			return usage("invalid personal token")
		}
		c := newClient(server, token)
		identity, err := c.identity(ctx)
		if err != nil {
			return err
		}
		if err = saveCredential(a.context, credential{server, token}); err != nil {
			return err
		}
		return a.output(map[string]any{"context": a.context, "identity": identity})
	}
	projectIDs := splitList(*projects)
	scopeIDs := splitList(*scopes)
	if len(projectIDs) > 32 {
		return usage("--projects must list at most 32 project UUIDs")
	}
	for _, id := range projectIDs {
		if err := validateUUID("project", id); err != nil {
			return usage(err.Error())
		}
	}
	for _, scope := range scopeIDs {
		if !slices.Contains(knownScopes, scope) {
			return usage("unknown token scope: " + scope)
		}
	}
	if !slices.Contains(scopeIDs, "catalog.read") {
		return usage("catalog.read is required for CLI identity and project selection")
	}
	if *ttl <= 0 || *ttl > 30*24*time.Hour || *ttl%time.Second != 0 {
		return usage("ttl must be whole seconds, greater than zero and at most 720h")
	}
	if len(*name) < 1 || len(*name) > 80 {
		return usage("token name must be 1–80 bytes")
	}
	if *org != "" {
		if err := validateUUID("organization", *org); err != nil {
			return usage(err.Error())
		}
	}
	c, password, err := a.sessionLogin(ctx, *email)
	if err != nil {
		return err
	}
	defer endSession(c)
	if *org == "" {
		res, err := c.json(ctx, "GET", "/api/auth/v1/me", nil)
		if err != nil {
			return err
		}
		var me struct {
			Organization struct{ ID string } `json:"organization"`
		}
		if err = json.Unmarshal(res.Body, &me); err != nil {
			return err
		}
		*org = me.Organization.ID
	}
	if err := validateUUID("organization", *org); err != nil {
		return usage("--organization is required for this account")
	}
	c.organization = *org
	// Validate access before issuing credentials; project IDs are explicit to avoid
	// accidentally granting newly-created projects to an unattended client.
	res, err := c.json(ctx, "GET", "/api/app/v1/projects", nil)
	if err != nil {
		return err
	}
	var available struct {
		Items []struct{ ID, Name string } `json:"items"`
	}
	if err = json.Unmarshal(res.Body, &available); err != nil {
		return err
	}
	if len(projectIDs) == 0 {
		if len(available.Items) == 0 {
			return errors.New("this account has no accessible projects")
		}
		fmt.Fprintln(a.Err, "Choose projects for this token:")
		for i, p := range available.Items {
			fmt.Fprintf(a.Err, "  %d. %s (%s)\n", i+1, safeText(p.Name, 120), safeText(p.ID, 64))
		}
		selection, err := a.ask("Project numbers (comma-separated)", false)
		if err != nil {
			return err
		}
		for _, number := range splitList(selection) {
			i, err := strconv.Atoi(number)
			if err != nil || i < 1 || i > len(available.Items) {
				return usage("invalid project selection")
			}
			projectIDs = append(projectIDs, available.Items[i-1].ID)
		}
		if len(projectIDs) == 0 || len(projectIDs) > 32 {
			return usage("select between 1 and 32 projects")
		}
	}
	for _, id := range projectIDs {
		if err := validateUUID("project", id); err != nil {
			return errors.New("server returned an invalid project identifier")
		}
		found := false
		for _, p := range available.Items {
			if p.ID == id {
				found = true
			}
		}
		if !found {
			return errors.New("a requested project is not accessible to this account")
		}
	}
	body, _ := json.Marshal(map[string]any{"name": *name, "organization_id": *org, "project_ids": projectIDs, "scopes": scopeIDs, "expires_in_seconds": int64(*ttl / time.Second)})
	path := "/api/auth/v1/me/personal-tokens"
	sum := sha256.Sum256(body)
	confirmation := map[string]any{"method": "POST", "path": path, "organization_id": *org, "body_sha256": hex.EncodeToString(sum[:])}
	res, err = c.json(ctx, "GET", "/api/auth/v1/me/security", nil)
	if err != nil {
		return err
	}
	var security struct {
		Email2FA bool `json:"email_two_factor_enabled"`
	}
	if err = json.Unmarshal(res.Body, &security); err != nil {
		return err
	}
	if !security.Email2FA {
		confirmation["password"] = password
	}
	res, err = c.json(ctx, "POST", "/api/auth/v1/auth/reauthenticate", confirmation)
	if err != nil {
		return err
	}
	if res.Status == 202 {
		var challenge struct {
			ID string `json:"challenge_id"`
		}
		if err = json.Unmarshal(res.Body, &challenge); err != nil || challenge.ID == "" {
			return errors.New("invalid confirmation challenge")
		}
		code, err := a.ask("Confirmation code from email", true)
		if err != nil {
			return err
		}
		c.secrets = append(c.secrets, code)
		confirmation["challenge_id"] = challenge.ID
		confirmation["code"] = code
		res, err = c.json(ctx, "POST", "/api/auth/v1/auth/reauthenticate", confirmation)
		if err != nil {
			return err
		}
	}
	var proof struct {
		Token string `json:"token"`
	}
	if err = json.Unmarshal(res.Body, &proof); err != nil || proof.Token == "" {
		return errors.New("confirmation did not return a proof")
	}
	c.secrets = append(c.secrets, proof.Token)
	res, err = c.request(ctx, "POST", path, body, http.Header{"X-Operation-Token": []string{proof.Token}})
	if err != nil {
		return err
	}
	var created struct {
		Token    string `json:"token"`
		Metadata struct {
			ID string `json:"id"`
		} `json:"personal_token"`
	}
	if err = json.Unmarshal(res.Body, &created); err != nil || !tokenPattern.MatchString(created.Token) {
		return errors.New("invalid token response")
	}
	c.secrets = append(c.secrets, created.Token)
	saved := false
	defer func() {
		if !saved && uuidPattern.MatchString(created.Metadata.ID) {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := c.json(cleanup, "DELETE", path+"/"+created.Metadata.ID, nil); err != nil {
				fmt.Fprintln(a.Err, "runnerctl: could not revoke the unsaved token; revoke it using auth revoke", created.Metadata.ID)
			}
		}
	}()
	identity, err := newClient(server, created.Token).identity(ctx)
	if err != nil {
		return err
	}
	if err = saveCredential(a.context, credential{server, created.Token}); err != nil {
		return err
	}
	saved = true
	return a.output(map[string]any{"context": a.context, "identity": identity})
}

func (a *App) sessionLogin(ctx context.Context, email string) (*client, string, error) {
	server, err := a.authServer()
	if err != nil {
		return nil, "", err
	}
	if email == "" {
		email, err = a.ask("Email", false)
		if err != nil {
			return nil, "", err
		}
	}
	password, err := a.ask("Password", true)
	if err != nil {
		return nil, "", err
	}
	c := newClient(server, "")
	c.enableSession()
	c.secrets = append(c.secrets, password)
	res, err := c.json(ctx, "GET", "/api/auth/v1/auth/csrf", nil)
	if err != nil {
		return nil, "", err
	}
	var csrf struct {
		Token string `json:"csrf_token"`
	}
	if err = json.Unmarshal(res.Body, &csrf); err != nil || csrf.Token == "" {
		return nil, "", errors.New("server did not supply CSRF token")
	}
	c.csrf = csrf.Token
	res, err = c.json(ctx, "POST", "/api/auth/v1/auth/login", map[string]string{"email": email, "password": password})
	if err != nil {
		return nil, "", err
	}
	if res.Status == 202 {
		var challenge struct {
			ID string `json:"challenge_id"`
		}
		if err = json.Unmarshal(res.Body, &challenge); err != nil || challenge.ID == "" {
			return nil, "", errors.New("invalid login challenge")
		}
		code, err := a.ask("Login code from email", true)
		if err != nil {
			return nil, "", err
		}
		c.secrets = append(c.secrets, code)
		res, err = c.json(ctx, "POST", "/api/auth/v1/auth/login/verify", map[string]string{"challenge_id": challenge.ID, "code": code})
		if err != nil {
			return nil, "", err
		}
	}
	if res.Status != 200 {
		return nil, "", errors.New("login did not establish a session")
	}
	return c, password, nil
}

func endSession(c *client) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = c.json(ctx, "POST", "/api/auth/v1/auth/logout", nil)
	c.http.Jar = nil
	c.csrf = ""
	c.secrets = nil
}
