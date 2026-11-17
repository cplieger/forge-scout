# Contributing to forge-scout

The [shared contributing rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Rules

Every forge API call goes through `internal/forgeconn`, the one package that imports forgeapi's role and row types. A GitHub read forgeapi does not model belongs in `internal/githubrest`, and the other forges report it as unsupported, never as 0.
