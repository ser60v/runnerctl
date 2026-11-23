# runnerctl

Manage Runner Platform pools and jobs from a terminal or an automation agent.
The CLI uses the same public API as the web application. A pool YAML holds
desired configuration; a personal token holds access to explicitly selected
projects in one organization.

## Install and sign in

Release archives contain a standalone `runnerctl` binary for Linux and macOS
(amd64/arm64), `VERSION`, and this README. Verify the archive against the release
`SHA256SUMS`, extract it, and put the binary on your PATH. Windows users can use
the Linux binary in WSL. Native Windows credential storage is not supported yet.

```sh
runnerctl auth login
runnerctl auth status
runnerctl projects list
```

Login prompts for your account password and, when enabled, email two-factor
codes, then lets you explicitly select accessible projects by number. You can
also pass `--organization ORGANIZATION_UUID --projects PROJECT_UUID,PROJECT_UUID`.
Issuance requires a fresh operation-bound confirmation. Passwords,
session cookies and confirmation proofs stay in memory; the bootstrap session
is logged out afterward. Only the issued PAT is saved, in a `0600` file inside
the private `0700` directory `$XDG_CONFIG_HOME/runnerctl` (or the platform's
user-config directory). Use a Unix filesystem that enforces these permissions.

Tokens default to seven days and expire within thirty days. Use `--ttl 24h`,
`--name build-agent`, and a narrower `--scopes` when appropriate. Available
scopes are `catalog.read`, `pools.read`, `pools.write`, `runs.read`, and
`runs.cancel`. The CLI requires `catalog.read` for identity/project discovery.
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
runnerctl init --organization ORGANIZATION_UUID --project PROJECT_UUID \
  --name linux-ci --concurrency 3 -f pool.yaml
runnerctl plan -f pool.yaml
runnerctl apply -f pool.yaml
```

`init` creates a unique pool ID once and refuses to overwrite a file. Keep that
ID in version control so subsequent applies update the same pool. Example:

```yaml
apiVersion: runner.ser60v.com/v1
kind: Pool
context: default
metadata:
  id: 33333333-3333-4333-8333-333333333333
  organization_id: 11111111-1111-4111-8111-111111111111
  project_id: 22222222-2222-4222-8222-222222222222
spec:
  name: linux-ci
  environment: ubuntu-latest
  site_id: ""
  cpu_millis: 2000
  memory_mib: 8192
  disk_mib: 41088
  max_job_minutes: 60
  max_runners: 3
  enabled: true
```

Resources are **per worker**, while `max_runners` is the pool concurrency cap.
Three workers with these values need 6 CPUs, 24 GiB RAM and 123,264 MiB reserved
disk. Available organization/project budgets and eligible sites must fit those
totals. Disk reservation includes site-specific execution overhead; it is not
necessarily the writable workspace size. Inspect `sites list` and `plan`
before lowering it. `init --cpu`, `--memory` and `--disk` accept mCPU/MiB;
the initial values above are a starting point, not a promise of capacity.
Use `--site SITE_ID` from `sites list` and `--environment ENVIRONMENT_ID` from
`environments list` to initialize an explicitly placed pool. Site identifiers
are names such as `desktop-i9`, not project UUIDs.

`plan` validates environment, site, storage requirements and quota through the
server without reserving resources. It returns the current revision, changes
and validation errors. `apply` obtains a fresh plan and uses `If-Match` for an
update or `If-None-Match: *` for creation. A concurrent UI/CLI change returns a
conflict instead of being overwritten; inspect a new plan and retry. Reapplying
an unchanged configuration performs no write. A pool cannot move projects.
The YAML has no hooks, interpolation, credentials or remote includes. Unknown
fields, duplicate keys, aliases and extra documents are rejected.

Use `--context staging` before `auth login` to save another context; put that
name in the manifest and use `--context staging` before `plan`/`apply` too.
The manifest must match the selected context and cannot switch credentials.
`RUNNERCTL_CONFIG_DIR` can isolate an agent's credentials.

## Inspect and operate

```sh
runnerctl pools list --project PROJECT_UUID
runnerctl pools get POOL_UUID
runnerctl pools delete POOL_UUID
runnerctl environments list
runnerctl sites list
runnerctl status
runnerctl workflows list --project PROJECT_UUID
runnerctl workflows jobs WORKFLOW_GROUP_UUID
runnerctl runs list --project PROJECT_UUID --status running --limit 50 --offset 0
runnerctl runs get JOB_UUID
runnerctl runs watch JOB_UUID --timeout 15m --interval 5s
runnerctl runs cancel JOB_UUID
```

`runs` are individual tracked jobs; `workflows` are grouped GitHub workflow
attempts. List responses include pagination totals where supported. Run status
and cancellation use platform UUIDs, not numeric GitHub job IDs. Cancelling an
unassigned job can return `job_not_assigned`: cancel that workflow in GitHub.
Pool deletion fetches and checks its revision. Public platform status needs no
token. Administrative accounts, secrets, billing and GitHub installations are
outside the personal-token route allowlist.

## Automation contract

Global flags precede the command:

```sh
runnerctl --json plan -f pool.yaml
runnerctl --json apply -f pool.yaml
runnerctl --json runs watch JOB_UUID --timeout 30m
```

Ordinary JSON commands emit one object; watch emits one compact JSON object per
observation (NDJSON) on stdout. Diagnostics and structured errors go to stderr.
Watch stops on success, failure, cancellation, job timeout or infrastructure
error. It obeys `Retry-After` for temporary `429`/`503` responses, has a finite
timeout, and exits on Ctrl-C. Mutations are not blindly retried.

| Exit | Meaning |
|---|---|
| 0 | Successful operation or completed successful job |
| 1 | Authentication, authorization, transport or other request failure |
| 2 | Invalid input/command/manifest |
| 3 | Conflict or stale revision (`409`/`412`) |
| 4 | Observed job ended unsuccessfully, including cancellation |
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

The Auth and API repositories contain the authoritative OpenAPI schemas. CLI
pool writes reuse the existing UI validation; there is no second scheduler or
quota implementation in this client.

## Development and release

Requires Go 1.27.4 and Unix filesystem permissions. Public dependencies are
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
race tests and the production binary. Use `GOCACHE`, `GOMODCACHE`, `GOTMPDIR`
and `BUILD_DIR` to place build data on suitable storage. Tests require actual
Unix permission semantics; Windows-mounted directories without metadata do not
provide those semantics. `make smoke` checks the binary and Compose contract,
including an inherited empty `SSH_AUTH_SOCK`.

`VERSION` is the only release version. CI checks every branch and publishes
checksummed Unix archives to the project's Generic Package Registry and a
GitLab release when `VERSION` changes on the default branch. CI artifacts expire
after seven days; released versions remain owned by Runner Platform maintainers
until deliberately retired. Local `.runtime`/`.build` are disposable developer
caches. The optional scratch container contains only the binary and CA roots,
runs as UID 65532 and needs no capabilities. CI builds it with rootless BuildKit
and publishes the immutable `VERSION` tag in the project's Container Registry.
This CLI is a short-lived client:
it has no listening port, health endpoint, Kubernetes workload or delivery-broker
deployment. `version` is its executable smoke check.
