package cli

import "encoding/json"

type commandSpec struct {
	Command string   `json:"command"`
	Args    string   `json:"arguments"`
	Scopes  []string `json:"scopes"`
	Mutates bool     `json:"mutates"`
	Public  bool     `json:"public"`
	Output  string   `json:"output"`
	Notes   string   `json:"notes,omitempty"`
}

func (a *App) printSchema() int {
	commands := []commandSpec{
		{Command: "help", Public: true, Output: "text; --json emits this contract"},
		{Command: "schema", Public: true, Output: "command contract JSON"},
		{Command: "version", Public: true, Output: "version"},
		{Command: "auth login", Args: "[--token-stdin] [--organization UUID] [--projects UUID,UUID] [--name NAME] [--scopes SCOPE,...] [--ttl 168h] [--email EMAIL]", Mutates: true, Output: "context", Notes: "Interactive account authentication mints a PAT; --token-stdin imports an existing PAT. Stores a private origin-bound credential."},
		{Command: "auth status", Output: "context"},
		{Command: "auth logout", Mutates: true, Output: "ok", Notes: "Removes local credentials only."},
		{Command: "auth tokens", Output: "tokens", Notes: "Interactive account authentication."},
		{Command: "auth revoke", Args: "TOKEN_ID", Mutates: true, Output: "ok", Notes: "Interactive account authentication."},
		{Command: "init", Args: "--organization UUID --project UUID [-f pool.yaml] [--name linux-ci] [--concurrency 3] [--environment ubuntu-latest] [--site SITE_ID] [--cpu 2000] [--memory 8192] [--disk 41088]", Mutates: true, Public: true, Output: "file, pool_id and next command", Notes: "Creates a local YAML; never executes configuration. CPU mCPU, memory/disk MiB per runner."},
		{Command: "plan", Args: "-f pool.yaml", Scopes: []string{"catalog.read", "pools.write"}, Output: "pool plan"},
		{Command: "apply", Args: "-f pool.yaml", Scopes: []string{"catalog.read", "pools.read", "pools.write"}, Mutates: true, Output: "action and pool", Notes: "Optimistic concurrency; exit 3 requires a fresh plan. No blind mutation retries."},
		{Command: "pools list", Args: "[--project UUID] [--limit 50] [--offset 0]", Scopes: []string{"pools.read"}, Output: "pools"},
		{Command: "pools get", Args: "UUID", Scopes: []string{"pools.read"}, Output: "pool"},
		{Command: "pools delete", Args: "UUID", Scopes: []string{"pools.read", "pools.write"}, Mutates: true, Output: "ok"},
		{Command: "runs list", Args: "[--project UUID] [--status STATUS] [--limit 50] [--offset 0]", Scopes: []string{"runs.read"}, Output: "runs page"},
		{Command: "runs get", Args: "UUID", Scopes: []string{"runs.read"}, Output: "run"},
		{Command: "runs cancel", Args: "UUID", Scopes: []string{"runs.cancel"}, Mutates: true, Output: "run"},
		{Command: "runs watch", Args: "UUID [--interval 5s] [--timeout 15m]", Scopes: []string{"runs.read"}, Output: "run observations (NDJSON with --json)", Notes: "Waits for job outcome; does not prove runner cleanup. jobs is an alias for runs."},
		{Command: "runners list", Args: "[--all] [--project UUID] [--pool UUID] [--status STATUS] [--limit 50] [--offset 0]", Scopes: []string{"runs.read"}, Output: "runner page", Notes: "Active API allocation projections by default; --all includes completed. Not a live VM probe."},
		{Command: "runners get", Args: "UUID", Scopes: []string{"runs.read"}, Output: "runner observation", Notes: "Attempts a bounded manager read; check observation_source, manager_status and nullable phase/site. Resources are reservations, not live consumption."},
		{Command: "runners watch", Args: "UUID [--interval 5s] [--timeout 15m]", Scopes: []string{"runs.read"}, Output: "runner observations (NDJSON with --json)", Notes: "Completes only on manager phase done and a terminal status. Manager unavailable continues until deadline. No control actions."},
		{Command: "workflows list", Args: "[--project UUID] [--status STATUS] [--limit 50] [--offset 0]", Scopes: []string{"runs.read"}, Output: "workflow attempts page"},
		{Command: "workflows jobs", Args: "UUID", Scopes: []string{"runs.read"}, Output: "workflow jobs page"},
		{Command: "projects list", Scopes: []string{"catalog.read"}, Output: "projects"},
		{Command: "environments list", Scopes: []string{"catalog.read"}, Output: "runner environments"},
		{Command: "sites list", Scopes: []string{"catalog.read"}, Output: "hardware"},
		{Command: "status", Public: true, Output: "platform status", Notes: "No credentials transmitted."},
		{Command: "status watch", Args: "[--interval 5s] [--timeout 15m]", Public: true, Output: "status observations (NDJSON with --json)", Notes: "Streams until finite deadline (5) or interrupt (130); no credentials transmitted."},
	}
	for i := range commands {
		if commands[i].Scopes == nil {
			commands[i].Scopes = []string{}
		}
	}
	schema := map[string]any{
		"schema_version": 1, "cli_version": a.Version, "commands": commands,
		"global_flags":          []string{"--context NAME", "--server HTTPS_ORIGIN", "--json"},
		"global_flags_position": "before command", "aliases": map[string]string{"jobs": "runs"},
		"environment":    []string{"RUNNERCTL_TOKEN", "RUNNERCTL_CONFIG_DIR"},
		"default_server": defaultServer, "documentation": defaultServer + "/docs/cli.md",
		"pagination": map[string]int{"default_limit": 50, "maximum_limit": 100, "maximum_offset": 1000000},
		"watch":      map[string]string{"minimum_interval": "1s", "maximum_interval": "1m", "maximum_timeout": "24h", "retry": "429/503 with Retry-After; finite timeout"},
		"exit_codes": map[string]string{"0": "success", "1": "request or access error", "2": "invalid input", "3": "conflict", "4": "unsuccessful observed job or runner", "5": "watch deadline", "130": "interrupted"},
		"streams":    map[string]string{"stdout": "result JSON or NDJSON observations with --json", "stderr": "diagnostics and JSON errors/warnings with --json"},
	}
	if err := json.NewEncoder(a.Out).Encode(schema); err != nil {
		return a.failure(err, 1)
	}
	return 0
}
