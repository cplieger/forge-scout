# Troubleshooting

This page lists the problem lines forge-scout logs, what each one means and how to fix it. It is for operators whose dashboard shows a gap or whose container will not start.

## The healthcheck

The healthcheck runs `/forge-scout health`, which checks that the scan loop refreshed its health file within the last three scan intervals plus one scan at its limit of twice the interval. That is 75 minutes at the default `scan_interval` of 15m. Unhealthy means the loop stopped, so restart the container.

The container stays healthy through a failed scan, a rejected token or a spent budget. A failed scan and a rejected token appear in the log, and the alert rules in [Monitoring and alerts](monitoring.md#alerting) fire when they repeat. A spent budget stops that connection's scan until the budget renews, and no alert fires for it. An unhealthy container notifies nobody by itself. `ForgeScoutScanStalled` is the alert for a stopped loop, and [Knowing the app is alive](monitoring.md#knowing-the-app-is-alive) says who is told.

## After the first start

Each connection ends its scan with one `"msg":"scan complete"` line, and the `connection` field names which one. If a forge rejected the token or could not be reached, its connection logs `"msg":"repo discovery failed"`, then `"msg":"scan degraded"`, and still ends with `"msg":"scan complete"`, but with `"degraded":true`. A connection that stopped early logs `"msg":"scan stopped"` and no `scan complete`.

## Problem lines and fixes

- If the container exits at start with `invalid configuration`, the error names the key or the connection to fix. A missing file names `/config/config.yaml`.
- If the log shows `"cause":"token_invalid"`, that forge rejected the token. Check that it has not expired.
- If the log shows `"cause":"owner_unresolved"`, that forge does not know one of the `owners`. The `reason` field names it.
- If the log shows `owner lists no repository`, the `owners` field names owners that exist but hold no repository this token can see. A misspelt owner that happens to name another account shows up here. So do a token that cannot see the owner's private repositories and a GitLab user whose profile is private.
- If the log shows `uses the same account as`, two connections to one forge use tokens of the same account. List both connections' owners in the first one and remove the second.
- If every scan logs `code scanning unreadable` for the same repository with `"cause":"github_refused"`, GitHub refuses that repository's code scanning. It does so for a private repository without GitHub Advanced Security once `security.include_private` is on. Add its path to `security.skip_repos`.
- If the log shows `"cause":"github_refused"` after `runs listing failed` or `pull request checks unreadable`, the token lacks a permission on that repository. Its `hint` field names the one to grant.
- If the log shows `runs coverage gap`, forge-scout was stopped for longer than `lookback`, or its run lists of that repository stayed cut short for that long. The CI runs created in the `hours` before the window were not read. If you expect such gaps, raise `lookback`. When the line carries `pending_since`, a list cut short no longer reached a run still in progress at the scan before, so that run's result may never be logged.
- If the log shows `run state unreadable; starting cold`, the file in `/data` could not be used and was moved to the `set_aside` path, so the first scan reports the whole lookback window again. The `error` field says why. Check that `./data` belongs to the `PUID` and `PGID` the container runs as, 1000 unless `.env` sets them.
- If the container exits at start with `run state directory unusable`, forge-scout could not create its lock file in `/data`. The `error` field says why. Check that `./data` exists and belongs to the `PUID` and `PGID` the container runs as.
- If the log shows `run state save failed`, `/data` became unwritable or full after forge-scout started. Each scan logs `"cause":"state_unwritable"`. CI runs are still logged, and a restart before a save succeeds logs them again. The `error` field says why.
- If the log shows `run state not durable`, the file system under `/data` cannot sync a folder, as some network shares cannot. Each scan logs `"cause":"state_not_durable"`. CI runs are still reported, but a power loss can make the next start report some of them again. Put `/data` on a local disk.
- If the container exits at start with `state directory is in use by another forge-scout process`, another forge-scout uses the same `/data`. Give each container its own folder. Stop the daemon before you run `trigger`.

[Scan summary and integrity](monitoring.md#scan-summary-and-integrity) lists every `cause` value and the fields of `scan complete`.
