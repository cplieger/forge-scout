# forge-scout

[![Image Size](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/forge-scout/badges/size.json)](https://github.com/cplieger/forge-scout/pkgs/container/forge-scout) [![Platforms](https://img.shields.io/badge/platforms-amd64%20%7C%20arm64-blue)](https://github.com/cplieger/forge-scout/pkgs/container/forge-scout) [![base: Distroless](https://img.shields.io/badge/base-Distroless_nonroot-4285F4?logo=google)](https://github.com/cplieger/forge-scout/blob/main/Dockerfile) [![Mutation](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/cplieger/forge-scout/badges/mutation.json)](https://github.com/cplieger/forge-scout/issues?q=label%3Agremlins-tracker) [![SBOM](https://img.shields.io/badge/SBOM-SPDX-1D4ED8)](https://github.com/cplieger/forge-scout/releases)

<!-- hub-overview BEGIN -->
forge-scout is a web UI over the open pull requests, open issues and failing CI workflows that wait on you across your repositories on GitHub, GitLab, Gitea and Forgejo, with code-scanning alerts on GitHub.

forge-scout is free software under the GPL-3.0-or-later license.
<!-- hub-overview END -->

## Status

forge-scout 4 is being rebuilt as a web app, and this branch does not read any forge yet. It reads no config file, token variable or run state file of forge-scout 3, and it ships no Grafana dashboard. The quick start, configuration and documentation pages return as the features they describe land.

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
