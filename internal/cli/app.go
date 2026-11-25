package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"go.yaml.in/yaml/v3"
)

type App struct {
	In              io.Reader
	Out, Err        io.Writer
	Version         string
	context, server string
	json            bool
	prompt          func(string, bool) (string, error)
}

func New(in io.Reader, out, err io.Writer, version string) *App {
	return &App{In: in, Out: out, Err: err, Version: version, context: "default"}
}

const help = `runnerctl — Runner Platform CLI

Global flags (before the command):
  --context NAME   saved context (default: default)
  --server URL     HTTPS origin; must match saved credentials
  --json           JSON output; watch emits NDJSON

Commands:
  auth login [--token-stdin]
             [--name NAME] [--scopes SCOPE,...] [--ttl 168h] [--email EMAIL]
  auth status | logout | tokens | revoke TOKEN_ID
  init --project UUID [-f pool.yaml] [--concurrency 3]
       [--memory 8gb] [--disk 40.125gb]
  plan -f pool.yaml
  apply -f pool.yaml
  pools list [--project UUID] [--limit 50] [--offset 0]
  pools get UUID | delete UUID
  runs list [--project UUID] [--status STATUS] [--limit 50] [--offset 0]
  runs get UUID | cancel UUID
  runs watch UUID [--interval 5s] [--timeout 15m]
  runners list [--all] [--project UUID] [--pool UUID] [--status STATUS]
               [--limit 50] [--offset 0]
  runners get UUID | watch UUID [--interval 5s] [--timeout 15m]
  workflows list [--project UUID] [--status STATUS] [--limit 50] [--offset 0]
  workflows jobs UUID [--limit 50] [--offset 0]
  projects list | environments list | sites list | status | version
  status watch [--interval 5s] [--timeout 15m]
  schema          offline JSON command contract (also: --json help)

RUNNERCTL_TOKEN supplies an ephemeral token; RUNNERCTL_CONFIG_DIR selects storage.
Tokens never belong in a pool YAML. auth logout removes local credentials only.
auth tokens/revoke use interactive account authentication.
Exit codes: 0 success, 1 request/error, 2 input, 3 conflict, 4 failed job,
            5 watch timeout, 130 interrupted.
`

func (a *App) flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(a.Err)
	return f
}

func (a *App) Run(ctx context.Context, args []string) int {
	f := a.flags("runnerctl")
	f.StringVar(&a.context, "context", a.context, "saved context")
	f.StringVar(&a.server, "server", "", "HTTPS origin")
	f.BoolVar(&a.json, "json", false, "JSON output")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if a.json {
				return a.printSchema()
			}
			fmt.Fprint(a.Out, help)
			return 0
		}
		return a.failure(err, 2)
	}
	args = f.Args()
	if len(args) == 0 || args[0] == "help" {
		if a.json {
			return a.printSchema()
		}
		fmt.Fprint(a.Out, help)
		return 0
	}
	if !contextName.MatchString(a.context) {
		return a.failure(errors.New("invalid context name"), 2)
	}
	if a.server != "" {
		var err error
		a.server, err = canonicalServer(a.server)
		if err != nil {
			return a.failure(err, 2)
		}
	}
	var err error
	switch args[0] {
	case "schema":
		if len(args) != 1 {
			return a.failure(usage("schema takes no arguments"), 2)
		}
		return a.printSchema()
	case "version":
		err = a.output(map[string]any{"version": a.Version})
	case "auth":
		err = a.auth(ctx, args[1:])
	case "init":
		err = a.initPool(args[1:])
	case "plan", "apply":
		err = a.manifestCommand(ctx, args[0], args[1:])
	case "status":
		err = a.statusCommand(ctx, args[1:])
	case "pools", "runs", "jobs", "runners", "workflows", "projects", "environments", "sites":
		err = a.resource(ctx, args[0], args[1:])
	default:
		err = &commandError{message: "unknown command: " + args[0], exit: 2}
	}
	if err == nil {
		return 0
	}
	exit := 1
	var ce *commandError
	var ae *APIError
	if errors.As(err, &ce) {
		exit = ce.exit
	}
	if errors.As(err, &ae) && (ae.Status == 409 || ae.Status == 412) {
		exit = 3
	}
	if errors.Is(err, context.DeadlineExceeded) {
		exit = 5
	}
	if errors.Is(err, context.Canceled) {
		exit = 130
	}
	return a.failure(err, exit)
}

type commandError struct {
	message string
	exit    int
}

func (e *commandError) Error() string { return e.message }
func usage(message string) error      { return &commandError{message: message, exit: 2} }

func (a *App) failure(err error, exit int) int {
	if a.json {
		var ae *APIError
		if !errors.As(err, &ae) {
			ae = &APIError{Code: "command_error", Message: err.Error()}
		}
		_ = json.NewEncoder(a.Err).Encode(map[string]any{"error": ae, "exit_code": exit})
	} else {
		fmt.Fprintln(a.Err, "runnerctl:", safeText(err.Error(), 1500))
	}
	return exit
}

func (a *App) output(value any) error {
	if raw, ok := value.(json.RawMessage); ok {
		if len(raw) == 0 {
			value = map[string]any{"ok": true}
		} else {
			var v any
			if err := json.Unmarshal(raw, &v); err != nil {
				return err
			}
			value = v
		}
	}
	enc := json.NewEncoder(a.Out)
	if !a.json {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(value)
}

func (a *App) client() (*client, error) {
	token := os.Getenv("RUNNERCTL_TOKEN")
	server := a.server
	if token != "" {
		if !tokenPattern.MatchString(token) {
			return nil, errors.New("RUNNERCTL_TOKEN is not a personal access token")
		}
		if server == "" {
			server = defaultServer
		}
	} else {
		c, err := loadCredential(a.context)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, errors.New("not signed in; run runnerctl auth login or set RUNNERCTL_TOKEN")
			}
			return nil, err
		}
		if server != "" && server != c.Server {
			return nil, errors.New("server does not match the credential origin; use another context")
		}
		server = c.Server
		token = c.Token
	}
	return newClient(server, token), nil
}

type cliContext struct {
	TokenID      string                          `json:"token_id"`
	Organization struct{ ID, Name, Role string } `json:"organization"`
	Projects     []struct{ ID, Name string }     `json:"projects"`
	Scopes       []string                        `json:"scopes"`
	ExpiresAt    string                          `json:"expires_at"`
}

func (c *client) identity(ctx context.Context) (cliContext, error) {
	res, err := c.json(ctx, "GET", "/api/app/v1/cli/context", nil)
	if err != nil {
		return cliContext{}, err
	}
	return decodeResponse[cliContext](res)
}

func (a *App) initPool(args []string) error {
	f := a.flags("init")
	org := f.String("organization", "", "optional organization UUID guard for existing workflows")
	project := f.String("project", "", "project UUID")
	file := f.String("f", "pool.yaml", "manifest destination")
	name := f.String("name", "linux-ci", "pool name")
	site := f.String("site", "", "site identifier (empty: automatic placement)")
	environment := f.String("environment", "ubuntu-latest", "runner environment")
	concurrency := f.Int("concurrency", 3, "parallel jobs")
	cpu := f.Int("cpu", 2000, "mCPU per job")
	memory := f.String("memory", "8gb", "memory per job (e.g. 8gb; bare integer is MiB)")
	disk := f.String("disk", "40.125gb", "disk reserved per job (e.g. 40gb; bare integer is MiB)")
	if err := f.Parse(args); err != nil {
		return usage(err.Error())
	}
	if len(f.Args()) > 0 {
		return usage("unexpected init arguments")
	}
	orgProvided := false
	f.Visit(func(option *flag.Flag) {
		if option.Name == "organization" {
			orgProvided = true
		}
	})
	if orgProvided {
		if err := validateUUID("organization", *org); err != nil {
			return usage(err.Error())
		}
	}
	if err := validateUUID("project", *project); err != nil {
		return usage(err.Error())
	}
	memoryMiB, err := parseSize(*memory)
	if err != nil {
		return usage("invalid --memory: " + err.Error())
	}
	diskMiB, err := parseSize(*disk)
	if err != nil {
		return usage("invalid --disk: " + err.Error())
	}
	if *concurrency < 1 || *concurrency > 1000 || *cpu < 500 || memoryMiB < 512 || diskMiB < 512 {
		return usage("invalid resource values")
	}
	if *site != "" && !sitePattern.MatchString(*site) {
		return usage("invalid site identifier")
	}
	enabled := true
	m := manifest{APIVersion: poolAPIVersion, Kind: "Pool", Context: a.context, Spec: poolSpec{Name: *name, Environment: *environment, SiteID: *site, CPU: *cpu, Memory: memoryMiB, Disk: diskMiB, MaxJobMinutes: 60, MaxRunners: *concurrency, Enabled: &enabled}}
	m.Metadata.ID = newUUID()
	m.Metadata.OrganizationID = *org
	m.Metadata.ProjectID = *project
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(*file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = fh.Write(data); err != nil {
		fh.Close()
		return err
	}
	if err = fh.Close(); err != nil {
		return err
	}
	return a.output(map[string]any{"file": *file, "pool_id": m.Metadata.ID, "next": "runnerctl plan -f " + *file})
}

type poolPlan struct {
	PoolID   string          `json:"pool_id"`
	Revision int64           `json:"revision"`
	Action   string          `json:"action"`
	Desired  json.RawMessage `json:"desired"`
	Actions  []struct {
		Field    string `json:"field"`
		From, To any
	} `json:"actions"`
	Errors []any `json:"errors"`
}

func (a *App) manifestCommand(ctx context.Context, command string, args []string) error {
	f := a.flags(command)
	file := f.String("f", "pool.yaml", "manifest")
	if err := f.Parse(args); err != nil {
		return usage(err.Error())
	}
	if len(f.Args()) != 0 {
		return usage("unexpected manifest arguments")
	}
	m, err := readManifest(*file)
	if err != nil {
		return usage(err.Error())
	}
	if a.context != m.Context {
		return usage("selected context does not match manifest context")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	// An explicit organization in an older manifest is an additional gateway
	// guard. The API authoritatively checks the token's current project access.
	c.organization = m.Metadata.OrganizationID
	path := "/api/app/v1/pools/" + m.Metadata.ID
	res, err := c.request(ctx, "POST", path+"/plan", m.body(), nil)
	if err != nil {
		return err
	}
	p, err := decodeResponse[poolPlan](res)
	if err != nil {
		return err
	}
	if p.PoolID != m.Metadata.ID || p.Revision < 0 || len(p.Errors) > 0 || (p.Action != "none" && p.Action != "create" && p.Action != "update") {
		return errors.New("invalid plan returned by server")
	}
	if command == "plan" {
		return a.output(res.Body)
	}
	headers := http.Header{}
	if p.Action == "create" {
		if p.Revision != 0 {
			return errors.New("invalid create revision")
		}
		headers.Set("If-None-Match", "*")
	} else {
		if p.Revision < 1 {
			return errors.New("invalid update revision")
		}
		headers.Set("If-Match", strconv.Quote(strconv.FormatInt(p.Revision, 10)))
	}
	// Send our validated intent; never execute or blindly trust server-supplied plan data.
	res, err = c.request(ctx, "PUT", path, m.body(), headers)
	if err != nil {
		return err
	}
	if p.Action == "none" {
		return a.output(map[string]any{"action": "none", "pool_id": p.PoolID, "revision": p.Revision})
	}
	var pool any
	if err = json.Unmarshal(res.Body, &pool); err != nil {
		return err
	}
	return a.output(map[string]any{"action": p.Action, "pool": pool})
}

func (a *App) resource(ctx context.Context, resource string, args []string) error {
	if resource == "jobs" {
		resource = "runs"
	}
	if len(args) == 0 {
		return usage(resource + " requires a subcommand")
	}
	sub := args[0]
	args = args[1:]
	if resource == "runs" && sub == "watch" {
		return a.watchCommand(ctx, args)
	}
	if resource == "runners" && sub == "watch" {
		return a.runnerWatchCommand(ctx, args)
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	paths := map[string]string{"pools": "pools", "runs": "runs", "runners": "runners", "workflows": "workflow-runs", "projects": "projects", "environments": "runner-environments", "sites": "hardware"}
	path := "/api/app/v1/" + paths[resource]
	method := "GET"
	var headers http.Header
	switch sub {
	case "list":
		f := a.flags(resource + " list")
		var project, status, pool string
		limit, offset := 50, 0
		var all bool
		if resource != "projects" && resource != "environments" && resource != "sites" {
			f.StringVar(&project, "project", "", "project UUID")
			f.IntVar(&limit, "limit", 50, "page size")
			f.IntVar(&offset, "offset", 0, "page offset")
		}
		if resource == "runs" || resource == "runners" || resource == "workflows" {
			f.StringVar(&status, "status", "", "status")
		}
		if resource == "runners" {
			f.StringVar(&pool, "pool", "", "pool UUID")
			f.BoolVar(&all, "all", false, "include completed allocations")
		}
		if err := f.Parse(args); err != nil {
			return usage(err.Error())
		}
		if len(f.Args()) > 0 {
			return usage("unexpected list arguments")
		}
		q := url.Values{}
		if resource == "runners" {
			q.Set("active", strconv.FormatBool(!all))
			if pool != "" {
				if err := validateUUID("pool", pool); err != nil {
					return usage(err.Error())
				}
				q.Set("pool_id", pool)
			}
			if status != "" && !runnerStatus(status) {
				return usage("unknown runner status")
			}
		}
		if project != "" {
			if err := validateUUID("project", project); err != nil {
				return usage(err.Error())
			}
			q.Set("project_id", project)
		}
		if status != "" {
			q.Set("status", status)
		}
		if resource != "projects" && resource != "environments" && resource != "sites" {
			if limit < 1 || limit > 100 || offset < 0 || offset > 1000000 {
				return usage("invalid pagination")
			}
			q.Set("limit", strconv.Itoa(limit))
			q.Set("offset", strconv.Itoa(offset))
		}
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
	case "get", "delete", "cancel", "jobs":
		if len(args) == 0 {
			return usage(sub + " requires one UUID")
		}
		if sub != "jobs" && len(args) != 1 {
			return usage(sub + " requires one UUID")
		}
		if err := validateUUID("id", args[0]); err != nil {
			return usage(err.Error())
		}
		if !((sub == "get" && (resource == "runs" || resource == "runners" || resource == "pools")) || (sub == "delete" && resource == "pools") || (sub == "cancel" && resource == "runs") || (sub == "jobs" && resource == "workflows")) {
			return usage("unsupported resource operation")
		}
		path += "/" + args[0]
		if sub == "jobs" {
			path += "/jobs"
			f := a.flags("workflows jobs")
			limit := f.Int("limit", 50, "page size")
			offset := f.Int("offset", 0, "page offset")
			if err := f.Parse(args[1:]); err != nil {
				return usage(err.Error())
			}
			if len(f.Args()) > 0 || *limit < 1 || *limit > 100 || *offset < 0 || *offset > 1000000 {
				return usage("invalid workflow jobs pagination")
			}
			q := url.Values{"limit": {strconv.Itoa(*limit)}, "offset": {strconv.Itoa(*offset)}}
			path += "?" + q.Encode()
		}
		if sub == "cancel" {
			path += "/cancel"
			method = "POST"
		}
		if sub == "delete" {
			res, err := c.request(ctx, "GET", path, nil, nil)
			if err != nil {
				return err
			}
			etag := res.Header.Get("ETag")
			if etag == "" {
				return errors.New("server did not supply a pool revision")
			}
			headers = http.Header{"If-Match": []string{etag}}
			method = "DELETE"
		}
	default:
		return usage("unknown subcommand: " + sub)
	}
	res, err := c.request(ctx, method, path, nil, headers)
	if err != nil {
		return err
	}
	if !a.json && resource == "runs" && sub == "list" {
		return a.printRuns(res.Body)
	}
	if !a.json && resource == "runners" && sub == "list" {
		return a.printRunners(res.Body)
	}
	return a.output(res.Body)
}

func (a *App) printRuns(raw json.RawMessage) error {
	var envelope struct {
		Items []struct {
			ID, Status string
			JobName    string  `json:"job_name"`
			Duration   float64 `json:"duration_seconds"`
		}
		Total int
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tDURATION\tJOB")
	for _, r := range envelope.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", safeText(r.ID, 64), safeText(r.Status, 40), duration(r.Duration), safeText(r.JobName, 160))
	}
	fmt.Fprintf(w, "Total: %d\n", envelope.Total)
	return w.Flush()
}

func duration(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	n := int64(seconds)
	if n >= 3600 {
		return fmt.Sprintf("%dh %dm %ds", n/3600, n/60%60, n%60)
	}
	if n >= 60 {
		return fmt.Sprintf("%dm %ds", n/60, n%60)
	}
	return fmt.Sprintf("%ds", n)
}

func terminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "timed_out", "infrastructure_error":
		return true
	}
	return false
}

func (a *App) watchCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage("watch requires a run UUID")
	}
	id := args[0]
	if err := validateUUID("run", id); err != nil {
		return usage(err.Error())
	}
	f := a.flags("runs watch")
	interval := f.Duration("interval", 5*time.Second, "poll interval")
	timeout := f.Duration("timeout", 15*time.Minute, "maximum duration")
	if err := f.Parse(args[1:]); err != nil {
		return usage(err.Error())
	}
	if len(f.Args()) > 0 || *interval < time.Second || *interval > time.Minute || *timeout <= 0 || *timeout > 24*time.Hour {
		return usage("interval must be 1s–1m and timeout must be greater than zero and at most 24h")
	}
	c, err := a.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	return a.watch(ctx, c, id, *interval)
}

func (a *App) watch(ctx context.Context, c *client, id string, interval time.Duration) error {
	for {
		res, err := c.json(ctx, "GET", "/api/app/v1/runs/"+id, nil)
		delay := interval
		if err != nil {
			var ae *APIError
			if !errors.As(err, &ae) || (ae.Status != 429 && ae.Status != 503) {
				return err
			}
			if ae.RetryAfter > delay {
				delay = ae.RetryAfter
			}
			a.retryNotice(delay)
		} else {
			var run struct {
				Status   string  `json:"status"`
				Duration float64 `json:"duration_seconds"`
			}
			if err = json.Unmarshal(res.Body, &run); err != nil || run.Status == "" {
				return errors.New("invalid run status response")
			}
			if a.json {
				var compact bytes.Buffer
				if err = json.Compact(&compact, res.Body); err != nil {
					return err
				}
				if _, err = fmt.Fprintln(a.Out, compact.String()); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(a.Out, "%s  %s  %s\n", time.Now().UTC().Format(time.RFC3339), safeText(run.Status, 40), duration(run.Duration))
			}
			if terminal(run.Status) {
				if run.Status != "succeeded" {
					return &commandError{message: "job ended with status " + run.Status, exit: 4}
				}
				return nil
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func splitList(raw string) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range strings.Split(raw, ",") {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			out = append(out, v)
			seen[v] = true
		}
	}
	return out
}
