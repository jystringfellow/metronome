# GitHub App authentication

For a shared service, use a GitHub App installation rather than a person's `gh` login. Create an App in
GitHub, choose only the permissions needed for the features below, install it on the repositories it needs,
and generate a private key. Use the App ID (or client ID) and the installation ID, not the ID of a person.

Configure Metronome through its environment (not the instance configuration or command arguments):

| Variable | Purpose |
| --- | --- |
| `METRONOME_GITHUB_APP_ID` | App ID or client ID. |
| `METRONOME_GITHUB_APP_INSTALLATION_ID` | Positive installation ID. |
| `METRONOME_GITHUB_APP_PRIVATE_KEY_FILE` | Path to a protected RSA private key. |
| `METRONOME_GITHUB_APP_PRIVATE_KEY` | Alternatively, the key contents supplied by a secret store. |
| `METRONOME_GITHUB_API_URL` | Optional API base URL, default `https://api.github.com`. |

Give exactly one of the key file and key contents. Both PKCS#1 and PKCS#8 RSA keys work.

For a protected key file:

```sh
export METRONOME_GITHUB_APP_ID=123
export METRONOME_GITHUB_APP_INSTALLATION_ID=456
export METRONOME_GITHUB_APP_PRIVATE_KEY_FILE=/run/secrets/github-app-key
metronome serve --config instance.json
```

Alternatively, have your secret store set `METRONOME_GITHUB_APP_PRIVATE_KEY` in the service environment
instead of setting the file variable. Do not print the key or put its contents in a command or values file.
For Helm, use `extraEnv` for the IDs and `secretEnv` for the key contents, or mount an existing Secret
through `extraVolumes` and `extraVolumeMounts` and set the file variable.

Metronome validates configuration before serving and mints an installation token on demand. Installation
tokens normally last an hour; Metronome refreshes about ten minutes early (half the lifetime for shorter
tokens). Transient request failures are retried with backoff. A still-valid cached token with more than a
minute remaining can be used if refreshing fails. With an App, `gh`, intake and publisher commands receive
the current token through `GH_TOKEN`, replacing inherited GitHub tokens, and never receive App key settings.
Runner commands do not receive App settings. With no App configured, existing `gh` authentication works
as before. Token endpoint failures only report safe categories and HTTP statuses; known issued tokens and
recognizable credentials are redacted from diagnostic output. Intake output and publisher state containing
known issued tokens are refused instead of persisted. Each successful mint forgets expired token values;
recognizable token formats are still redacted from diagnostics. Commands must never print or persist credentials.

For GitHub Enterprise Server, configure `METRONOME_GITHUB_API_URL=https://github.example.com/api/v3`
and `GH_HOST=github.example.com`. Metronome also sets `GH_ENTERPRISE_TOKEN` for non-public API hosts;
this configuration has not been verified against a live GHES installation.

## App permissions

Grant the union of the applicable permissions, restricted to the repositories the installation needs.
The following permissions are inferred from the API operations used by Metronome; live permission checks
are still unverified.

| Feature | Repository permissions | Organisation permissions |
| --- | --- | --- |
| PR poll and adoption | Pull requests, Checks, Commit statuses, Contents, Metadata: read | None |
| Issues intake | Issues, Pull requests, Metadata: read | Projects: read if using `--project` |
| Project publisher | Issues: read/write; Metadata: read | Projects: read/write; Members: read where needed for assignees |

The publisher needs `--assignee LOGIN` with an installation token, because there is no signed-in person.
A GitHub App cannot reach a Project owned by a user account. Use an organisation-owned Project or run that
intake or publisher using a person's token.

## Running on Kubernetes

With the [Helm chart](kubernetes.md), replace the `GH_TOKEN` entry with these values. The existing Secret
contains the private key, not an installation token:

```yaml
extraEnv:
  - name: METRONOME_GITHUB_APP_ID
    value: "123"
  - name: METRONOME_GITHUB_APP_INSTALLATION_ID
    value: "456"
secretEnv:
  - name: METRONOME_GITHUB_APP_PRIVATE_KEY
    secretName: metronome-github
    key: app-key
```

Metronome refreshes installation tokens for the loops. Give an App publisher `--assignee pat`.
