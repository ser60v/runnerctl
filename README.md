# runnerctl

Manage Runner Platform pools, jobs and runner allocations from a terminal or an automation agent.
The CLI uses the same public API as the web application. A pool YAML holds
desired configuration; a personal token holds access to explicitly selected
projects in one organization.

The [public client source](https://github.com/ser60v/runnerctl) contains the
CLI, installers and packaging code. The platform API and backend are separate.
The client has no analytics, telemetry or background upload. A command sends
its required API request to the configured server, including the token for
authenticated routes; the server can observe the request and connecting IP.
The client is licensed under [MIT](LICENSE).

## Install and sign in

[Public release metadata](https://runners.retardsoftware.com/downloads/runnerctl/latest/metadata.json)
provides versioned standalone archives for Linux, macOS and Windows on amd64 and
arm64. For a current-user installation, use the matching installer:

```sh
curl --proto '=https' --tlsv1.2 -fsS https://runners.retardsoftware.com/downloads/runnerctl/install.sh | sh && export PATH="$HOME/.local/bin:$PATH"
```

In Windows PowerShell:

```powershell
irm https://runners.retardsoftware.com/downloads/runnerctl/install.ps1 | iex
```

For a review before execution, download
[`install.sh`](https://runners.retardsoftware.com/downloads/runnerctl/install.sh)
or [`install.ps1`](https://runners.retardsoftware.com/downloads/runnerctl/install.ps1),
read it, then run `sh install.sh` or `.\install.ps1`. PowerShell's usual script
execution policy applies to a saved file; the one-line command does not require
a global policy change. The installers select the platform archive, verify its
SHA-256 digest against the versioned `SHA256SUMS`, inspect archive entries, and
install into `~/.local/bin` or `%LOCALAPPDATA%\Programs\runnerctl`. The Unix
one-liner updates the current shell's PATH; the installer prints the profile
line to make it persistent. The Windows installer adds its directory to the
current process and current user's Path without changing the machine Path.
They do not use `sudo` or request elevation.
You can also download and verify an archive manually using the metadata.
Checksums and archives come from the same HTTPS origin; read
[`SECURITY.md`](SECURITY.md) for the trust limits and reporting process.

```sh
runnerctl auth login
runnerctl auth status
runnerctl projects list
```

Login prompts for your account password and, when enabled, email two-factor
codes, then lets you explicitly select accessible projects by number. You can
also pass `--projects PROJECT_UUID,PROJECT_UUID`. Your account supplies the organization.
Issuance requires a fresh operation-bound confirmation. Passwords,
session cookies and confirmation proofs stay in memory; the bootstrap session
is logged out afterward. Only the issued PAT is saved in the current user's
platform credential store. On Unix it is a `0600` file inside the private
`0700` directory `$XDG_CONFIG_HOME/runnerctl` (or the platform's user-config
directory). Use a Unix filesystem that enforces these permissions. On Windows
it is protected by the current user's DPAPI profile.

Tokens default to seven days and expire within thirty days. Use `--ttl 24h`,
`--name build-agent`, and a narrower `--scopes` when appropriate. Available
scopes are `catalog.read`, `pools.read`, `pools.write`, `runs.read`, and
`runs.cancel`. `auth status` and token import require `catalog.read` for
identity verification; interactive login also uses it for project discovery.
Pool `plan` and `apply` require `pools.write` and let the server check project access.
An observer can use `--scopes catalog.read,pools.read,runs.read`. The server
rechecks current account status, membership, project access and token revocation
on requests. A token cannot grant permissions its owner no longer has.

`runnerctl auth tokens` lists active tokens after interactive account login.
`runnerctl auth revoke TOKEN_UUID` revokes one; it also uses account login.
`runnerctl auth logout` deletes the local credential only and does not revoke
the server token or unset environment variables.

For an existing token, feed it directly from your secret manager into
`runnerctl auth login --token-stdin`. For an unattended agent, inject
`RUNNERCTL_TOKEN` through its secret environment. Neither option requires a
password or browser session. Do not put a token in YAML, command arguments,
source control or chat. The default origin is
`https://runners.retardsoftware.com`; use `--server` before a command for another
HTTPS origin. Saved credentials are bound to that origin and are never sent
through redirects. HTTP is allowed only on explicit loopback addresses for
development.

An environment token uses the fixed default origin unless `--server` is
explicitly supplied; a saved context or YAML cannot redirect it elsewhere.

## A pool as one YAML file

```sh
runnerctl sites list
runnerctl init --project PROJECT_UUID \
  --name linux-ci --concurrency 3 --site SITE_ID --disk 20gb -f pool.yaml
runnerctl plan -f pool.yaml
runnerctl apply -f pool.yaml
```

`init` creates a unique pool ID once and refuses to overwrite a file. Keep that
ID in version control so subsequent applies update the same pool. For this
20 GB example, choose a site with configurable disk from `sites list` and use
its ID for `SITE_ID` and `site_id` below. Example:

```yaml
apiVersion: runner.retardsoftware.com/v1
kind: Pool
context: default
metadata:
  id: 33333333-3333-4333-8333-333333333333
  project_id: 22222222-2222-4222-8222-222222222222
spec:
  name: linux-ci
  environment: ubuntu-latest
  site_id: your-site
  cpu_millis: 2000
  memory: 8gb
  disk: 20gb
  max_job_minutes: 60
  max_runners: 3
  enabled: true
```

`runnerctl` 0.1.4 and later generate `runner.retardsoftware.com/v1`. Existing
manifests using `runner.ser60v.com/v1` remain supported with the same behavior.

`plan` and `apply` use the organization of your signed-in token. The server
verifies current access to the selected project. No organization identifier is
needed in the YAML. An explicit `organization_id` in an older manifest remains
an additional request guard.

Resources are **per worker**, while `max_runners` is the pool concurrency cap.
Three workers with these values need 6 CPUs, 24 GB RAM and 60 GB reserved
disk. Available organization/project budgets and eligible sites must fit those
totals. Disk reservation includes site-specific execution overhead; it is not
necessarily the writable workspace size. Inspect `sites list` and `plan`
before lowering it. For automatic placement, omit `--site` and `--disk` to
keep the default disk profile. `init --cpu` accepts mCPU. `init --memory` and `--disk`
accept sizes such as `8gb`, `20gb`, `1,5 GB`, and `512mb`. Surrounding
spaces and a space before the unit are accepted. Decimal comma is accepted;
grouping separators are not. Units are case insensitive:
`mb`/`mib`, `gb`/`gib`, and `tb`/`tib` use binary multiples of MiB. A bare
integer is MiB for compatibility. Values must resolve to a positive whole MiB;
fractional MiB, negative values, exponents, and overflow are rejected. Pool
YAML uses `memory` and `disk`; existing `memory_mib` and `disk_mib` fields
remain accepted as integers or unit strings. Supplying both names for one
resource is rejected. API and `--json` resource fields remain integer MiB.
For example, `20gb` is 20480 MiB and `1.5gb` is 1536 MiB;
the initial values above are a starting point, not a promise of capacity.
Use `--site SITE_ID` from `sites list` and `--environment ENVIRONMENT_ID` from
`environments list` to initialize an explicitly placed pool. Site identifiers
are names such as `desktop-wsl-i9`, not project UUIDs.

`plan` validates environment, site, storage requirements and quota through the
server without reserving resources. It returns the current revision, changes
and validation errors. `apply` obtains a fresh plan and uses `If-Match` for an
update or `If-None-Match: *` for creation. A concurrent UI/CLI change returns a
conflict instead of being overwritten; inspect a new plan and retry. Reapplying
an unchanged configuration leaves pool state and revision unchanged; the CLI
still sends a conditional request so a concurrent change is detected. A pool
cannot move projects.
The YAML has no hooks, interpolation, credentials or remote includes. Unknown
fields, duplicate keys, aliases and extra documents are rejected.

Use `--context staging` before `auth login` to save another context; put that
name in the manifest and use `--context staging` before `plan`/`apply` too.
The manifest must match the selected context and cannot switch credentials.
`RUNNERCTL_CONFIG_DIR` can isolate an agent's credentials.

## Inspect and operate

```sh
runnerctl pools list --project PROJECT_UUID --limit 50 --offset 0
runnerctl pools get POOL_UUID
runnerctl pools delete POOL_UUID
runnerctl environments list
runnerctl sites list
runnerctl status
runnerctl status watch --interval 5s --timeout 15m
runnerctl workflows list --project PROJECT_UUID
runnerctl workflows jobs WORKFLOW_GROUP_UUID --limit 50 --offset 50
runnerctl runs list --project PROJECT_UUID --status running --limit 50 --offset 0
runnerctl runs get JOB_UUID
runnerctl runs watch JOB_UUID --timeout 15m --interval 5s
runnerctl runs cancel JOB_UUID
runnerctl runners list --project PROJECT_UUID --pool POOL_UUID
runnerctl runners list --all
runnerctl runners get RUNNER_UUID
runnerctl runners watch RUNNER_UUID --timeout 15m
```

`runs` are individual tracked jobs; `workflows` are grouped GitHub workflow
attempts. List responses include pagination totals where supported. Run status
and cancellation use platform UUIDs, not numeric GitHub job IDs. Cancelling an
unassigned job can return `job_not_assigned`: cancel that workflow in GitHub.
Pool deletion fetches and checks its revision. Public platform status needs no
token. Administrative accounts, secrets, billing and GitHub installations are
outside the personal-token route allowlist.

Runner lists contain durable API allocation records, including allocations whose VM
may not yet exist. `--all` includes completed records. Detail and watch attempt a
bounded Manager read; inspect `observation_source`, `manager_status`, and nullable
`phase`/`site_id`. `manager_observed_at` is fetch time, not a VM heartbeat.
`started_at` and duration describe allocation accounting; `running` is a controller
phase and does not alone prove the runner process started. CPU, memory and disk
are reservations, not live usage. A job's `execution_id` identifies its assigned
runner when known. `trigger_run_id` is a provisioning trigger, not an assignment.

The unauthenticated [web guide](https://runners.retardsoftware.com/docs/cli) and
[agent guide](https://runners.retardsoftware.com/docs/cli.md) cover setup and
troubleshooting. `runnerctl --json help` or `runnerctl schema` provides the
offline, versioned command contract without credentials or network access.

## Automation contract

Global flags precede the command:

```sh
runnerctl --json plan -f pool.yaml
runnerctl --json apply -f pool.yaml
runnerctl --json runs watch JOB_UUID --timeout 30m
runnerctl --json runners watch RUNNER_UUID --timeout 30m
runnerctl schema
```

Ordinary JSON commands emit one object; watch emits one compact JSON object per
observation (NDJSON) on stdout. Diagnostics and structured errors go to stderr.
Run watch stops on a terminal job outcome. Runner watch additionally waits for
a verified Manager `done` phase: cleanup and accounting must have finished.
An API terminal projection or unavailable Manager alone cannot complete it.
Public `status watch` streams until its finite deadline or interruption. Watch obeys `Retry-After` for temporary `429`/`503` responses, has a finite
timeout, and exits on Ctrl-C. Mutations are not blindly retried.

| Exit | Meaning |
|---|---|
| 0 | Successful operation or successful observed job/runner |
| 1 | Authentication, authorization, transport or other request failure |
| 2 | Invalid input/command/manifest |
| 3 | Conflict or stale revision (`409`/`412`) |
| 4 | Observed job/runner ended unsuccessfully, including cancellation |
| 5 | Watch deadline expired |
| 130 | Interrupted |

Error JSON is `{"error":{"code":"...","message":"...","status":412,"request_id":"..."},"exit_code":3}`;
HTTP status/request ID are present when available. Agents should use codes,
not parse prose. Duration in API JSON remains seconds; human run output uses
hours/minutes/seconds.

## Public API and boundaries

PAT requests use `Authorization: Bearer rp_pat_…` without cookies.
The Gateway introspects each credential through Auth, obtains a fresh signed
grant, and forwards it to API. Both Gateway and API enforce the route scopes;
API intersects the grant's project IDs with current project ACLs. Internal JWT
grants cannot be used as public PATs. Existing cookie/CSRF browser flows remain
in place.

| Route | Purpose |
|---|---|
| `POST /api/auth/v1/me/personal-tokens` | Issue PAT using session, CSRF and operation proof |
| `GET /api/auth/v1/me/personal-tokens` | List own active token metadata using session |
| `DELETE /api/auth/v1/me/personal-tokens/{id}` | Revoke own token using session/CSRF |
| `GET /api/app/v1/cli/context` | Current identity, accessible projects, scopes and expiry |
| `GET /api/app/v1/pools/{id}` | Read a pool and its ETag |
| `POST /api/app/v1/pools/{id}/plan` | Validate a complete desired pool without writes |
| `PUT /api/app/v1/pools/{id}` | Create/update a stable pool ID with preconditions |
| `GET /api/app/v1/runners` | Project-scoped allocation list with pagination and filters |
| `GET /api/app/v1/runners/{id}` | Allocation detail with bounded Manager observation |

The Auth and API repositories contain the authoritative OpenAPI schemas. CLI
pool writes reuse the existing UI validation; there is no second scheduler or
quota implementation in this client.

## Development and release

Requires Go 1.27.4. Public dependencies are
locked in `go.mod`/`go.sum`. Every Make entry point ensures a safe local `.env`;
the CLI does not load shell commands or credentials from that file.

```sh
make fmt
make check
make dev          # local help
make up           # build and run the disposable Compose version smoke
make down
```

`make check` verifies dependencies, formatting, vet, unit/HTTP boundary tests,
race tests, Unix installer behavior and the production binary. Python 3 is needed
for the installer tests. Use `GOCACHE`, `GOMODCACHE`, `GOTMPDIR`
and `BUILD_DIR` to place build data on suitable storage. Tests require actual
Unix permission semantics; Windows-mounted directories without metadata do not
provide those semantics. `make smoke` checks the binary and Compose contract,
including an inherited empty `SSH_AUTH_SOCK`.

GitHub Actions runs the checks and cross-builds all six archives on the owner's
Runner Platform Linux pool, selected by the `RUNNERCTL_RUNNER_LABEL` repository
variable. Cross-building does not execute macOS or Windows tests; native tests
can be run with `go test ./...` on those systems. Workflow tokens are read-only
and checkout credentials are not persisted.

`VERSION` is the only release version. GitLab release CI checks every branch and publishes
checksummed Linux, macOS and Windows archives to the project's Generic Package Registry and a
GitLab release when `VERSION` changes on the default branch. CI artifacts expire
after seven days; released versions remain owned by Runner Platform maintainers
until deliberately retired. Local `.runtime`/`.build` are disposable developer
caches. The optional scratch container contains only the binary and CA roots,
runs as UID 65532 and needs no capabilities. CI builds it with rootless BuildKit
and publishes the immutable `VERSION` tag in the project's Container Registry.
This CLI is a short-lived client:
it has no listening port, health endpoint, Kubernetes workload or delivery-broker
deployment. `version` is its executable smoke check.
