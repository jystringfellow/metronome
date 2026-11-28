# Making a release

The public image is `ghcr.io/jystringfellow/metronome`. The proposed release workflow,
`.github/workflows/release.yml`, must be added and reviewed by a maintainer outside the agent
sandbox, where CI files are protected. Until that happens, automatic publication is not enabled.

After the main branch passes CI, a maintainer tags its chosen commit `vMAJOR.MINOR.PATCH`
(or `vMAJOR.MINOR.PATCH-rc.N` for a prerelease) and pushes that tag. The workflow checks formatting,
runs vet and race tests, smoke-tests the container, and builds linux/amd64 and linux/arm64 images.
It publishes a multi-platform index with BuildKit SBOM and provenance attestations and signs
the published digest with keyless Cosign using the GitHub Actions OIDC identity. Main builds are
published separately; pull requests test the image without publishing or signing.

| Trigger | Image tags |
| --- | --- |
| `v1.4.2` | `1.4.2`, `1.4`, `1`, `latest` |
| `v0.3.0` | `0.3.0`, `0.3`, `latest` |
| `v1.5.0-rc.1` | `1.5.0-rc.1` |
| Push to main | `edge`, `main-<short sha>` |
| Pull request | None published |

Only stable releases update `latest`. Publish stable tags in release order, since a later workflow
run can move the rolling tags. Pin an exact version or digest rather than a rolling tag for repeatable
deployments. See [container usage](container.md) for signature and SBOM verification.

After the first successful push, a maintainer must check that the registry package is linked to the
public repository, allows that repository's Actions workflow to publish, and is set to **public**.
Verify that the image can be pulled without signing in. The workflow uses the repository's
`GITHUB_TOKEN` for publication, not a long-lived personal token.

Update `GO_VERSION` in the Dockerfile when the major/minor Go version in `go.mod` changes. Review
`ALPINE_VERSION` and `GH_VERSION` for supported updates and rebuild both architectures. The gh
download is pinned by release version and verified against that release's checksum list before
extraction.
