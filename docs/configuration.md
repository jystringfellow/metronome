# Running an instance from one file

Declare the workstreams and their intake, publish and GitHub polling schedules in a JSON file. Start the
service with `metronome serve --db metronome.db --config instance.json`.

```json
{
  "version": 1,
  "actor": "metronome",
  "workstreams": [
    {
      "name": "example",
      "limits": {"ready": 10, "plan": 3, "execute": 2, "review": 6, "release": 2},
      "adopt_label": "metronome:adopt",
      "intake": {
        "every": "10m",
        "command": ["metronome", "github", "issues", "--repo", "acme/app", "--project", "acme/3", "--instance", "hosted", "--owner", "pat"]
      },
      "publish": [
        {"name": "board", "every": "1m", "batch": 100,
         "command": ["metronome", "github", "publish", "--project", "acme/3", "--instance", "hosted"]}
      ]
    }
  ],
  "github_poll": {"every": "1m", "actor": "github", "adopt_repos": ["acme/app"]}
}
```

## File contract

- `version`: required, currently `1`. Version 1 evolves by adding optional fields with defaults that preserve
  existing meaning; incompatible changes require a new version. Unknown fields and trailing JSON values are refused.
- `actor`: defaults to the CLI's global `--as`, `METRONOME_ACTOR` or `$USER`. Each loop can override it.
  The resolved instance actor applies workstream settings.
- `workstreams`: defaults to an empty array. Each `name` is unique and follows the workstream naming rule
  (1–64 lowercase letters, digits, dots, underscores or hyphens, starting with a letter or digit).
- `limits`: optional nonnegative integers, including zero. On creation the defaults are Ready 10, Plan 3,
  Execute 5, Review 8 and Release 2. Omitted limits on existing streams are left alone.
- `adopt_label`: omitted leaves the label alone; an empty string clears it. Nonempty labels must be unique,
  ignoring case and surrounding whitespace, at most 50 characters, with no comma.
- `intake`: optional single command. `every` is a required positive Go duration (e.g. `10m`), `command` is a
  required nonempty argument array, and `actor` is optional.
- `publish`: optional list of publishers, each with `every`, `command` and optional `actor` as for intake,
  a unique `name` within its workstream (same naming rule), and `batch` (default 100, range 1–1000).
- `github_poll`: optional, with required positive `every`, optional `actor` and optional `adopt_repos`
  (an array of repositories in `OWNER/NAME` form, default empty).

## Secrets and commands

Supply credentials such as `GH_TOKEN` through the environment of `serve` using the operator's secret store.
Never put secret values in the file or command arguments. There is no `env` or token field. Commands inherit
the service environment and the existing [intake and publish](intake.md) variables. Arguments are passed
directly to the executable, without shell expansion. An explicit `sh -c` is the operator's choice; scripts
must still read secrets from their environment. Commands must not print secrets or include them in output.
Treat the configuration as trusted operator input, not a file supplied by an untrusted user.

## Startup and supervision

The file is validated before opening the database. After replay, the service creates missing workstreams
and updates declared limits and labels through normal tracker commands. Undeclared workstreams and publisher
cursors are not removed. Changes use existing events; restarting unchanged adds no workstream events.
If applying a setting fails, the service exits without starting any loops. Earlier settings may have been
applied; the next start can converge after the configuration is corrected.

Each loop runs immediately and independently, with no overlapping rounds of that loop. On success the
next round waits the configured interval minus the elapsed round time (at least zero). Errors and panics
retry after one second, doubling on consecutive failures and capped at the configured interval. Success
resets the backoff. Logs include the loop name, round number, elapsed time, status and retry delay on failure;
command output is prefixed with its loop name. Metronome does not log the command arguments or environment.

SIGINT and SIGTERM stop scheduling rounds, cancel running commands and wait for loops to exit before closing
the database. Changes to the file take effect on restart; there is no live reload. Removing a loop stops it
on restart without resetting its cursor. Running without `--config` leaves the loops to the operator as before.
