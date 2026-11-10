# Security policy

*Nederlands: meld een kwetsbaarheid vertrouwelijk via een [beveiligingsadvies](https://github.com/helmerzNL/NAS-Storage-Analyzer/security/advisories/new), nooit in een openbaar issue. De rest van dit document is in het Engels.*

## Reporting a vulnerability

Report it privately through a [security advisory](https://github.com/helmerzNL/NAS-Storage-Analyzer/security/advisories/new)
(the *Report a vulnerability* button under the repository's *Security* tab). Please do not open a public issue or pull request for it.

If that page is not available to you, open an issue that says only that you have a security report and how to reach you, with no
details of the problem.

Useful in a report: the version (the *About* screen, or `GET /api/v1/version`), how it is deployed (Compose, the all-in-one image,
behind which proxy), what you did and what happened. A proof of concept helps; a fix is welcome but not expected.

NASSA is maintained by one person in spare time. Allow a few days for a first answer.

## Supported versions

| Version | Supported |
|---|---|
| The latest release (image tag `latest`, and its version number) | Yes |
| The `beta` channel | Yes; a fix lands here first |
| Older releases | No: update to the latest release |

A fix is published as a new release. Earlier versions are not patched.

## What is deliberate

These are properties of the design, described in [`docs/deploy.md`](docs/deploy.md), and not vulnerabilities on their own:

- An administrator can point a share at any host the worker can reach, and with the mounts override
  (`docker-compose.mounts.yml`) the worker runs as root with `CAP_SYS_ADMIN`, `CAP_DAC_READ_SEARCH` and `CAP_DAC_OVERRIDE`.
  The recommended route mounts on the host and gives the containers no privileges.
- The application makes one outgoing request of its own, the update check to `github.com`, which can be switched off
  (`NASSA_UPDATE_CHECK=false`).
- TLS is terminated by a reverse proxy; the application does not serve HTTPS itself.

A way for someone who is **not** an administrator to do any of this, or for an administrator to reach beyond it (reading file
contents, writing to a share, leaving the container), is a vulnerability and very welcome as a report.

## How the project watches what it is built on

| Control | Where | What it checks |
|---|---|---|
| `supply-chain` | every pull request | no known vulnerability reachable from the Go code (`govulncheck`), no high or critical advisory in the npm packages (`npm audit`), no secret in the repository or its history (`gitleaks`) |
| image scan | every pull request, jobs `docker-build` and `aio-image` | no vulnerability of high or critical severity that has a fix, in the operating-system packages and the binary (Anchore `grype`) |
| `supply-chain-weekly` | every Monday | all of the above again without a push, the published `latest` and `beta` images, and whether the Alpine and Go release lines are still supported |
| Dependabot | weekly | pull requests for the Go modules, the npm packages and the GitHub Actions |

GitHub Actions are pinned to a commit, the scanner image to a digest. `go.sum` and `package-lock.json` are committed.

## What is in an image

Images built by the publish workflows carry a software bill of materials and a build provenance record, attached in the registry
(from the first build after v0.3.1; older images have none):

```bash
docker buildx imagetools inspect ghcr.io/helmerznl/nas-storage-analyzer:latest --format '{{ json .SBOM }}'
```

```bash
docker buildx imagetools inspect ghcr.io/helmerznl/nas-storage-analyzer:latest --format '{{ json .Provenance }}'
```

They say what was built and from which commit. They are not signed, so they are information, not proof against a tampered registry.
