# What each forge supports

This page lists what forge-scout reads on each forge, what a forge does not offer and what those reads cost. It is for operators who run more than one kind of forge or watch their API budget.

## Signals per forge

Pull requests, issues and CI runs are read on every forge. The CI runs are each forge's own: GitHub Actions, GitLab CI/CD pipelines, Gitea Actions and Forgejo Actions. Code-scanning alerts and disabled workflows exist on GitHub alone. On the other forges those counts are left out of the log. The count tiles leave them out, and What the newest scan read shows Not on this forge, never 0.

- On Gitea and Forgejo, the run listing has no time filter. [forgeapi](https://github.com/cplieger/forgeapi), the Go library forge-scout reads every forge through, ends the listing at the first page whose runs all predate the listing's start by more than a minute. A long run history then costs one or two requests per repository each scan.
- Gitea releases before 28 send no run creation time, so their runs are not read and the log says so once per scan.
- Run durations need a start time from the forge. GitHub, Gitea and Forgejo send one. GitLab's pipeline list does not, so GitLab workflows never appear among the slowest workflows.
- A GitLab owner that is a user and not a group has no owner-wide list of pull requests or issues. Then forge-scout reads each of that user's projects, one request per page of its open pull requests and one per page of its open issues.
- A pull request's checks are read on GitHub and GitLab, one request per open pull request.
- On a private repository, GitHub refuses pull request checks to a fine-grained token. The dashboard shows them as Not available with this token. Only a classic token with the `repo` scope reads them, as [Choosing a GitHub token](configuration.md#choosing-a-github-token) explains.
- Gitea and Forgejo name no head commit in the list of pull requests across repositories, so their pull requests show no checks.

## Request cost

[API use](how-it-works.md#api-use) lists the requests each scan makes per connection. It also covers the 250 requests forge-scout holds back of each budget a forge reports, and how the read cache saves requests on GitHub.
