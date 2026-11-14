# forge-scout

[![Image Size](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/forge-scout/badges/size.json)](https://github.com/cplieger/forge-scout/pkgs/container/forge-scout) [![Platforms](https://img.shields.io/badge/platforms-amd64%20%7C%20arm64-blue)](https://github.com/cplieger/forge-scout/pkgs/container/forge-scout) [![base: Distroless](https://img.shields.io/badge/base-Distroless_nonroot-4285F4?logo=google)](https://github.com/cplieger/forge-scout/blob/main/Dockerfile) [![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/forge-scout/badges/mutation.json)](https://github.com/cplieger/forge-scout/issues?q=label%3Agremlins-tracker) [![SBOM](https://img.shields.io/badge/SBOM-SPDX-1D4ED8)](https://github.com/cplieger/forge-scout/releases)

<!-- hub-overview BEGIN -->
forge-scout puts the open pull requests, open issues and failing CI workflows of your repositories on GitHub, GitLab, Gitea and Forgejo on one Grafana dashboard, with a link on every row. It adds code-scanning alerts on GitHub and only reads from your forges.

![The forge-scout Grafana dashboard's Overview tab: one tile for each thing to act on, such as a scan stopped at the API budget, failing workflows, critical and high code-scanning alerts and pull requests from other people, above the table of workflows failing on their default branch](docs/images/header.png)

## What it does

forge-scout shows what waits for you across all your repositories and forges:

- Lists open pull requests and issues, idlest first, with author, age and failing checks.
- Lists the workflows whose latest run on the default branch failed, since when and how many runs in a row.
- Shows every finished CI run, the slowest workflows and the workflows GitHub disabled.
- Lists open GitHub code-scanning alerts by severity.
- Shows a count it could not read whole as Partial, never as 0.

## Who it is for

forge-scout is built for people with repositories on GitHub, GitLab, Gitea or Forgejo, hosted or self-managed, private ones included. It scans every repository of the owners you name every 15 minutes, new ones included.

It reports only through its log, so you need Grafana 13.2 or newer, Loki and a log collector, which the [monitoring guide](https://github.com/cplieger/docs/blob/main/docs/monitoring.md#the-starting-stack) sets up. You also need a token for each forge, read-only where the forge allows it.

Two other tools suit a different need:

- Consider the [Grafana GitHub data source](https://grafana.com/grafana/plugins/grafana-github-datasource/) if you want Grafana panels that query the GitHub API directly.
- Consider [gh-dash](https://github.com/dlvhdr/gh-dash) if you want to work through GitHub pull requests and issues in a terminal.

forge-scout is free software under the GPL-3.0-or-later license.
<!-- hub-overview END -->

## Quick start

The image is on GitHub Container Registry and Docker Hub, for `amd64` and `arm64`. This is the [`compose.yaml`](compose.yaml) in this repository.

```yaml
services:
  forge-scout:
    image: ghcr.io/cplieger/forge-scout:latest
    container_name: forge-scout
    restart: unless-stopped
    # Must own the ./data host folder. If .env sets PUID and PGID, those numbers are used.
    user: "${PUID:-1000}:${PGID:-1000}"

    environment:
      # Every token the config file names as ${...}. Put each in a .env file beside this file.
      # The github connection in config.example.yaml reads this token. Delete this line when you remove that connection.
      GITHUB_TOKEN: "${GITHUB_TOKEN:-}"
      # The forgejo connection in config.example.yaml reads this token. Delete this line when you remove that connection.
      FORGEJO_TOKEN: "${FORGEJO_TOKEN:-}"

    volumes:
      # Run "mkdir -p config && cp config.example.yaml config/config.yaml", then edit
      # config/config.yaml before the first start.
      - "./config:/config:ro"
      # The run state. Run "mkdir -p data && sudo chown 1000:1000 data" before the first start.
      # If .env sets PUID and PGID, use those numbers.
      - "./data:/data"
```

1. Create a token on each forge with the [permissions](docs/configuration.md#token-permissions) it needs. On GitHub, a fine-grained token with read-only Actions, Contents, Commit statuses, Pull requests, Issues and Code scanning alerts is the least access. It has no Checks permission, so pull request checks on private repositories show as Not available with this token. It reads the private repositories of one resource owner, the user or organization you choose when you create it, and code-scanning alerts only on its resource owner's repositories. [Choosing a GitHub token](docs/configuration.md#choosing-a-github-token) says when you need a classic token with the `repo` scope instead, for private repositories under several owners or code scanning on another owner's public repositories. That scope also grants write access.
2. In the folder that holds `compose.yaml`, create a file named `.env` with one line per token, such as `GITHUB_TOKEN=github_pat_your_token`. If your user on the host is not uid 1000, also add `PUID=` and `PGID=` lines with the numbers `id -u` and `id -g` print.
3. Save [`config.example.yaml`](config.example.yaml) beside `compose.yaml`, then run `mkdir -p config && cp config.example.yaml config/config.yaml`. In `config/config.yaml`, set each connection's `url` and `owners`, and delete the connections you do not use.
4. In the `environment:` block of `compose.yaml`, keep one line for each token variable the config names. For a connection the example does not have, add a line such as `GITLAB_TOKEN: "${GITLAB_TOKEN:-}"`.
5. Run `mkdir -p data && sudo chown 1000:1000 data`, using your `PUID` and `PGID` when you set them. Then run `docker compose up -d`.
6. Have your log collector [send the container's logs to Loki](https://github.com/cplieger/docs/blob/main/docs/monitoring.md#the-starting-stack) with the label `container="forge-scout"`, which every panel selects on.
7. In Grafana, [import](https://github.com/cplieger/docs/blob/main/docs/monitoring.md#importing-an-apps-dashboard) [`grafana-dashboard.json`](grafana-dashboard.json). It reads from your default Loki data source.

Run `docker logs forge-scout`. You should see one `"msg":"scan complete"` line with `"degraded":false` per connection. If a forge rejected the token or could not be reached, that connection logs `"msg":"repo discovery failed"`, and its `scan complete` line carries `"degraded":true`. [After the first start](docs/troubleshooting.md#after-the-first-start) explains a `"msg":"scan degraded"` or `"msg":"scan stopped"` line.

## Configuration reference

The settings are one YAML file, `/config/config.yaml` or the path in `CONFIG_PATH`, read once at start, so recreate the container after a change. When a name ends in `_TOKEN` or starts with `FORGE_SCOUT_`, a value written as `${NAME}`, such as `${GITHUB_TOKEN}`, takes that environment variable's value. An unknown key stops the start with an error naming it.

| Variable | Description | Default |
| --- | --- | --- |
| `CONFIG_PATH` | Path of the config file inside the container | `/config/config.yaml` |
| Each `${NAME}` the file names | The value of that reference, such as `GITHUB_TOKEN`, set in the `environment:` block | required |
| `PUID`, `PGID` | The user and group the container runs as, who must own `./data` | `1000` |

The file takes these keys:

| Key | Default | Description |
| --- | --- | --- |
| `scan_interval` | `15m` | Time between scans, from 1 minute to 171 hours |
| `lookback` | `72h` | How far back each scan reads CI runs, from `1h` to `720h`. It must be at least 4.2 times `scan_interval` |
| `log_level` | `info` | `debug` or `info`. The dashboard needs the `info` lines, so `warn` and `error` stop the start |
| `exclude` | none | Rules that leave out open pull requests and issues on every connection ([exclusions](docs/configuration.md#leaving-repositories-and-items-out)) |
| `connections` | required | One entry per forge instance, keys below |

Each connection takes these keys:

| Key | Default | Description |
| --- | --- | --- |
| `name` | required | The `connection` label of every log line. Lowercase letters, digits, `-` and `_`, up to 32 characters |
| `url` | required | The address you open the forge at, such as `https://github.com` |
| `api_url` | derived from `url` | The API address, for an instance that serves it somewhere else |
| `token` | required | A read-only token, written as exactly one `${NAME}` reference |
| `owners` | required | The users, organizations or GitLab groups whose repositories are scanned |
| `exclude_repos` | none | Full `owner/name` paths left out of every signal |
| `exclude` | none | More rules like the top-level `exclude`, for this connection |
| `bot_authors` | none | Logins shown as bots although the forge counts them as people |
| `security` | `enabled: true`, `skip_forks: true`, `include_private: false` | `enabled`, `skip_forks`, `include_private` and `skip_repos` for code-scanning alerts |
| `private_addresses` | `false` | Allow an instance on a private or loopback address |
| `allow_plaintext` | `false` | Allow an `http://` address |

| Mount | Description |
| --- | --- |
| `/config` | The config file, mounted read-only |
| `/data` | The run state and its lock file |

forge-scout detects which product each connection runs. [Configuration](docs/configuration.md) explains the tokens, owners, exclusions and the one-shot `trigger` command.

## What each forge supports

Code-scanning alerts and disabled workflows exist on GitHub alone. GitLab workflows never appear among the slowest workflows, and Gitea and Forgejo pull requests show no checks. [What each forge supports](docs/forges.md) lists every difference and its request cost.

## Security

forge-scout opens no port. It sends only read requests, and only to the forges you configure, so give each token the least access its forge allows. A token travels only in the request header to its own forge and never appears in the log. Keep `.env` out of git.

It reads at most 8 MiB of each response, so a hostile or broken server cannot fill its memory. It stops a scan before fewer than 250 requests remain under the [rate limit](docs/how-it-works.md#api-use) a forge reports, so other tools that share it keep those requests. A connection needs `private_addresses: true` for a private or loopback address, and `allow_plaintext: true` for `http://`.

The image is distroless, with no shell, and runs as a non-root user. It writes only to `/data` and to its health file in `/dev/shm`, so it runs on a read-only root. [Security](docs/hardening.md) has the hardened compose settings and what the image contains.

## Troubleshooting

The healthcheck runs `/forge-scout health`, which fails when the scan loop has not refreshed its health file for 75 minutes, at the default `scan_interval`. Unhealthy means the loop stopped, so restart the container. An unhealthy container notifies nobody by itself, so `ForgeScoutScanStalled` is the alert for a stopped loop. A failed scan or a rejected token leaves the container healthy, and the [alert rules](docs/monitoring.md#alerting) fire when it repeats. A scan stopped at the 250-request reserve fires no alert, and the next scan after the rate limit resets reads again.

- If the container exits at start with `invalid configuration`, the error names the key or the connection to fix.
- If the log shows `"cause":"token_invalid"`, that forge rejected the token. Check that it has not expired.
- If the log shows `"cause":"owner_unresolved"` or `owner lists no repository`, check the spelling of `owners` and that the token can see their repositories.
- If the log shows `run state directory unusable` or `run state unreadable; starting cold`, check that `./data` exists and belongs to the `PUID` and `PGID` the container runs as.

[Troubleshooting](docs/troubleshooting.md) covers every other problem line and its fix.

## Monitoring

forge-scout writes one JSON log line for each open pull request, issue and code-scanning alert, each CI run that finished or changed state, and a `scan complete` summary per connection after each scan. The bundled dashboard reads them from Loki, and two Loki alert rules fire when a connection's scans go blind or no scan finishes. [Monitoring and alerts](docs/monitoring.md) lists the fields, the dashboard and the rules.

## Documentation

- [Configuration](docs/configuration.md) covers token permissions, owners, exclusions and one-shot scans.
- [What each forge supports](docs/forges.md) covers the differences between forges and their request cost.
- [Troubleshooting](docs/troubleshooting.md) covers each problem log line and its fix.
- [Monitoring and alerts](docs/monitoring.md) covers the log lines, Grafana dashboard and alert rules.
- [Security](docs/hardening.md) covers the hardened compose settings and what the image contains.
- [How forge-scout works](docs/how-it-works.md) covers scanning, the run state and API use.

## Credits

forge-scout reads every forge through [forgeapi](https://github.com/cplieger/forgeapi), a Go library for GitHub, GitLab, Gitea and Forgejo, and GitHub code scanning and workflows through the [GitHub REST API](https://docs.github.com/en/rest). The way that GitHub client authenticates, sets the API version and pages through results follows two Prometheus exporters for GitHub, [githubexporter/github-exporter](https://github.com/githubexporter/github-exporter) and [xrstf/github_exporter](https://github.com/xrstf/github_exporter).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Disclaimer

This project is built with care and follows security best practices, but it is intended for personal / self-hosted use. No guarantees of fitness for production environments. Use at your own risk.

This project was built with AI assistance. The human maintainer defines architecture, supervises implementation, and makes all final decisions.

## License

GPL-3.0-or-later. See [LICENSE](LICENSE). The image carries the license text of every bundled component under `/usr/share/licenses/`.

Third-party attributions are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
