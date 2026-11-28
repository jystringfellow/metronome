# Running Metronome in a container

The generic Alpine-based image contains `metronome`, `gh`, CA certificates and `tini`. It supports
linux/amd64 and linux/arm64, runs as UID and GID 65532, and defaults to
`serve --addr 0.0.0.0:7340 --db /data/metronome.db`. Other commands replace that default command.

Once the [release workflow](releasing.md) is installed and the package is public, pull the image from
`ghcr.io/jystringfellow/metronome`. Pin a published version (or its digest) in production. Minor tags
track patch releases; `edge` follows main. The examples use `1.4.2` as an illustrative release tag;
replace it with a published tag.

## Persistent data

```sh
IMAGE=ghcr.io/jystringfellow/metronome:1.4.2
docker volume create metronome-data
docker run -d --name metronome -p 127.0.0.1:7340:7340 \
  -v metronome-data:/data "$IMAGE"
```

The board and API are not authenticated. Publish only to loopback or put an authenticating proxy
in front of both; do not expose the API directly to an untrusted network.

Run one server per database. SQLite uses WAL, so mount the whole `/data` directory on a local
filesystem, not NFS or SMB. A named volume starts with the image's directory ownership. For a
bind mount, create a dedicated directory and grant UID/GID 65532 write access:

```sh
mkdir -p metronome-data
sudo chown 65532:65532 metronome-data
docker run -d --name metronome -p 127.0.0.1:7340:7340 \
  -v "$PWD/metronome-data:/data" "$IMAGE"
```

Use one of these examples, not both. Stop and remove the container before replacing it, keeping the
volume. For a filesystem backup, stop the server before copying the database directory, including
any `-wal` and `-shm` files; do not copy a live database's files independently.

## Run the loops alongside the server

For a fresh setup (or after stopping and removing the previous server container), use a private
Docker network. Keep the volume from the named-volume example, and provide `GH_TOKEN` from your
secret store to the GitHub clients. Do not bake credentials into images. Client commands explicitly
set `--as` or `METRONOME_ACTOR` because `$USER` may be unset.

```sh
docker network create metronome
docker run -d --name metronome --network metronome -p 127.0.0.1:7340:7340 \
  -v metronome-data:/data "$IMAGE"
docker run --rm --network metronome -e METRONOME_URL=http://metronome:7340 \
  "$IMAGE" --as pat workstream create example
docker run -d --name metronome-poll --network metronome -e GH_TOKEN \
  -e METRONOME_URL=http://metronome:7340 "$IMAGE" --as github github poll --every 1m
docker run -d --name metronome-intake --network metronome -e GH_TOKEN \
  -e METRONOME_URL=http://metronome:7340 -e METRONOME_ACTOR=intake "$IMAGE" \
  intake --workstream example --every 10m -- metronome github issues \
  --repo acme/app --project acme/3 --instance hosted --owner pat
docker run -d --name metronome-publish --network metronome -e GH_TOKEN \
  -e METRONOME_URL=http://metronome:7340 -e METRONOME_ACTOR=publish "$IMAGE" \
  publish --workstream example --name board --every 1m -- metronome github publish \
  --project acme/3 --instance hosted
```

Wait for the server to start before creating the workstream or running the loops. Skip workstream
creation if it already exists. Give each token the permissions its command needs; see
[intake and publishing](intake.md) and [the poller](runners.md). Only the server accesses the
database. The hosted shadow deployment does not yet accept writes from laptops.

## Extend the image

Organisations can add other intake tools (for example, the Azure CLI for `metronome ado items`)
in their own Dockerfile and registry. Here is an example adding `jq` for a custom source command:

```dockerfile
FROM ghcr.io/jystringfellow/metronome:1.4.2
USER root
RUN apk add --no-cache jq
USER 65532:65532
```

```sh
docker build -t registry.example.com/acme/metronome:1.4.2 .
```

Keep the inherited entrypoint and default command, and return to the non-root user after installing
tools. Keep organisation-specific scripts, configuration and credentials outside the public repository.

## Verify a release

```sh
cosign verify \
  --certificate-identity-regexp '^https://github.com/jystringfellow/metronome/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com "$IMAGE"
docker buildx imagetools inspect "$IMAGE" --format '{{ json .SBOM }}'
```

## Build locally

From the repository root, with Docker and Buildx installed:

```sh
docker build -t metronome .
docker buildx build --platform linux/amd64,linux/arm64 .
```

The latter needs a builder supporting both target platforms (for example with emulation).

To exercise the native image export and the two-platform OCI export with SBOM and provenance,
run `METRONOME_TEST_IMAGE_BUILD=1 go test -count=1 -timeout=30m -run '^TestContainerBuild$' ./cmd/metronome`.
This needs registry access and an OCI-capable builder. Both archives are temporary; neither is pushed.
