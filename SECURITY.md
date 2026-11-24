# Security policy

`runnerctl` is a client for the Runner Platform public API. A personal access
token can act on the projects and scopes granted to it until it expires or is
revoked. Keep tokens out of command arguments, source files, logs and support
reports. Use the interactive login, `--token-stdin`, or a secret manager that
supplies `RUNNERCTL_TOKEN`. Revoke a token in the account UI or with
`runnerctl auth revoke` if you suspect exposure.

The [public source](https://github.com/ser60v/runnerctl) and CI are intended
to make the thin client auditable. This repository contains the client,
installers and packaging code; the platform API and backend are operated
separately. The client has no analytics, telemetry or background upload path.
It sends the selected command's API requests to the configured server, with
the token and fields needed for that command. The service can observe those
requests and the connecting IP address.

Release archives and installers are served from
`https://runners.retardsoftware.com/downloads/runnerctl/`. The installers
fetch a versioned archive and its `SHA256SUMS` from that fixed HTTPS origin,
verify the archive digest, inspect archive members, and install only the binary
for the current user. They do not request administrator privileges or change
machine security settings. The checksum catches corruption and a mismatch
between assets. Because the checksum and archive share one origin, it does
not prove authenticity if that origin or its release pipeline is compromised.
Review the installer before running it, especially when using the one-line
pipe or `Invoke-Expression` examples.

To report a vulnerability, use the public repository's private vulnerability
reporting feature in its [Security tab](https://github.com/ser60v/runnerctl/security/advisories/new). You can also contact the Runner Platform operators through your established private support channel. Do not put exploit details, tokens, customer data or
workflow logs in a public issue. Include the affected CLI version, platform,
reproduction steps and impact, with synthetic data where possible.
