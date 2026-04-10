# epoch

A general-purpose OCI Distribution registry that hosts every flavor of
[cocoonstack](https://github.com/cocoonstack) artifact in one place:

- **Container images** — `oras` / `crane` / `docker` push and pull as usual.
- **OCI cloud images** — disk-only artifacts (qcow2 / raw, including split parts)
  with `artifactType: application/vnd.cocoonstack.os-image.v1+json`. The windows
  builder publishes these to `ghcr.io/cocoonstack/windows/win11:25h2` and the
  same shape works in epoch.
- **OCI VM snapshots** — cocoon VM state captured by `cocoon vm save` and
  uploaded by `epoch push` as an OCI artifact with
  `artifactType: application/vnd.cocoonstack.snapshot.v1+json`.

Epoch is **vendor-agnostic at the storage layer** — the `registry` package
only knows about blobs, manifests, and a global catalog. Cocoonstack-specific
concepts (snapshot, cloudimg) live in the `snapshot/` and `cloudimg/`
sub-packages on top.

## Overview

- **Content-addressed storage** — blobs deduplicated by SHA-256 digest
- **OCI 1.1 Distribution API** — `/v2/` push/pull works with `oras`, `crane`,
  `docker`, `containerd`, `buildah`, and any OCI-compliant client
- **OCI artifact classification** — top-level `artifactType` (cocoonstack
  cloud-image / snapshot) plus a `config.mediaType` fallback for plain
  container images
- **No filesystem coupling** — `epoch push` and `epoch pull` pipe through
  `cocoon snapshot export` / `cocoon snapshot import` / `cocoon image import`,
  so epoch never reads `/var/lib/cocoon` directly
- **MySQL metadata index** — queryable catalog for the web UI and control API
- **SSO login** — optional Google OAuth or generic OIDC for the web UI
- **Token management** — create and revoke bearer tokens from the dashboard

## Architecture

```text
                          oras / crane / docker
                                  |
                                  ▼
                    ┌──────────────────────────┐
                    │ Epoch HTTP server        │
       ┌────────────│ /v2/  /api/  /dl/{name}  │────────────┐
       │            └──────────────────────────┘            │
       ▼                          ▼                          ▼
┌───────────────┐        ┌─────────────────┐        ┌─────────────────┐
│ snapshot pkg  │        │ registry pkg    │        │ cloudimg pkg    │
│ Push / Pull   │◄──────►│ blob/manifest   │◄──────►│ Stream / Pull   │
│ (cocoon pipe) │        │ + catalog.json  │        │ (cocoon pipe)   │
└───────────────┘        └─────────────────┘        └─────────────────┘
                                  │
                                  ▼
                          S3 / GCS bucket
```

Object layout in the bucket (under prefix `epoch/`):

```text
catalog.json                            — global repository index
manifests/<repo>/<tag>.json             — manifest by tag
manifests/<repo>/_digests/<dgst>.json   — manifest by content digest
blobs/sha256/<dgst>                     — content-addressable blob
```

## Artifact format

All three artifact kinds are **standard OCI 1.1 image manifests**. Epoch
classifies them by looking at:

| Field | Value | Kind |
|---|---|---|
| `artifactType` | `application/vnd.cocoonstack.os-image.v1+json` | cloud image |
| `artifactType` | `application/vnd.cocoonstack.snapshot.v1+json` | snapshot |
| `config.mediaType` | `application/vnd.oci.image.config.v1+json` | container image |
| `config.mediaType` | `application/vnd.docker.container.image.v1+json` | container image |
| `mediaType` (top) | OCI image index / Docker manifest list | container image (multi-arch) |

A snapshot manifest looks like:

```json
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.manifest.v1+json",
  "artifactType": "application/vnd.cocoonstack.snapshot.v1+json",
  "config": {
    "mediaType": "application/vnd.cocoonstack.snapshot.config.v1+json",
    "digest": "sha256:...",
    "size": 123
  },
  "layers": [
    { "mediaType": "application/vnd.cocoonstack.vm.config+json", "digest": "sha256:...", "annotations": {"org.opencontainers.image.title": "config.json"} },
    { "mediaType": "application/vnd.cocoonstack.vm.state+json", "digest": "sha256:...", "annotations": {"org.opencontainers.image.title": "state.json"} },
    { "mediaType": "application/vnd.cocoonstack.vm.memory",     "digest": "sha256:...", "annotations": {"org.opencontainers.image.title": "memory-ranges"} },
    { "mediaType": "application/vnd.cocoonstack.disk.qcow2",    "digest": "sha256:...", "annotations": {"org.opencontainers.image.title": "overlay.qcow2"} }
  ],
  "annotations": {
    "cocoonstack.snapshot.id": "sid-...",
    "cocoonstack.snapshot.baseimage": "ghcr.io/cocoonstack/cocoon/ubuntu:24.04",
    "org.opencontainers.image.created": "2026-04-09T..."
  }
}
```

A cloud-image manifest follows the same shape but uses
`artifactType: application/vnd.cocoonstack.os-image.v1+json` and only carries
disk layers (`vnd.cocoonstack.disk.qcow2[.part]` / `vnd.cocoonstack.disk.raw[.part]`).

## Installation

### Download

Grab a release tarball from [GitHub Releases](https://github.com/cocoonstack/epoch/releases).
Set `VERSION` to the release you want (the archive filename embeds it):

```bash
VERSION=0.1.6   # pick the latest release tag from the Releases page

# Linux (amd64)
curl -fSL https://github.com/cocoonstack/epoch/releases/download/v${VERSION}/epoch_${VERSION}_Linux_x86_64.tar.gz \
  | tar -xzf - epoch
sudo install -m 0755 epoch /usr/local/bin/epoch && rm epoch

# Linux (arm64)
curl -fSL https://github.com/cocoonstack/epoch/releases/download/v${VERSION}/epoch_${VERSION}_Linux_arm64.tar.gz \
  | tar -xzf - epoch
sudo install -m 0755 epoch /usr/local/bin/epoch && rm epoch

# macOS (Apple Silicon)
curl -fSL https://github.com/cocoonstack/epoch/releases/download/v${VERSION}/epoch_${VERSION}_Darwin_arm64.tar.gz \
  | tar -xzf - epoch
sudo install -m 0755 epoch /usr/local/bin/epoch && rm epoch

# macOS (Intel)
curl -fSL https://github.com/cocoonstack/epoch/releases/download/v${VERSION}/epoch_${VERSION}_Darwin_x86_64.tar.gz \
  | tar -xzf - epoch
sudo install -m 0755 epoch /usr/local/bin/epoch && rm epoch
```

### Build from source

```bash
git clone https://github.com/cocoonstack/epoch.git
cd epoch
make build          # produces ./epoch
```

## Configuration

### Object storage

| Variable | Description |
|---|---|
| `EPOCH_S3_ENDPOINT` | S3 endpoint (with or without scheme) |
| `EPOCH_S3_ACCESS_KEY` | Access key |
| `EPOCH_S3_SECRET_KEY` | Secret key |
| `EPOCH_S3_BUCKET` | Bucket name |
| `EPOCH_S3_REGION` | Region (optional) |
| `EPOCH_S3_SECURE` | `true` or `false`; inferred from scheme if omitted |
| `EPOCH_S3_PREFIX` | Key prefix (default `epoch/`) |
| `EPOCH_S3_ENV_FILE` | Env file path (default `~/.config/epoch/s3.env`) |

### Authentication

**Registry clients** (`/v2/`):

- Bearer token from `EPOCH_REGISTRY_TOKEN` or tokens created via the UI
- Tokens are validated by SHA-256 hash against MySQL

**Web UI and control API**:

- Disabled by default (open access)
- Set `SSO_PROVIDER=google` or `SSO_PROVIDER=oidc` to enable session-based login
- See [epoch-server.yaml](epoch-server.yaml) for the full list of SSO variables

### Upload spool

In-progress chunked OCI uploads are spooled to disk so multi-GiB layers do
not pin RAM. The directory MUST be backed by real disk — `tmpfs` (the
default `/tmp` on most systemd hosts) defeats the spool and will OOM the
host on big pushes.

| Variable | Description |
|---|---|
| `EPOCH_UPLOAD_DIR` | Spool directory (default `/var/cache/epoch/uploads`; falls back to `os.TempDir()` with a loud warning if neither is creatable) |

The bundled `epoch-server.service` already sets `CacheDirectory=epoch` and
exports `EPOCH_UPLOAD_DIR=/var/cache/epoch/uploads`, so systemd deploys are
configured out of the box.

### Deployment files

| Path | Purpose |
|---|---|
| `docker-compose.yaml` | Local MySQL and MinIO |
| `epoch-server.yaml` | Kubernetes Deployment template |
| `Dockerfile` | Container image build |
| `epoch-server.service` | systemd unit file |
| `epoch-nginx.conf` | nginx vhost — required tuning for streaming multi-GiB OCI pushes |

## Quick Start

Start local dependencies:

```bash
export MYSQL_ROOT_PASSWORD=changeme
export MYSQL_PASSWORD=changeme
export MINIO_ROOT_USER=minioadmin
export MINIO_ROOT_PASSWORD=changeme
docker compose up -d
```

Build and run:

```bash
make build

export EPOCH_S3_ENDPOINT=http://127.0.0.1:9000
export EPOCH_S3_ACCESS_KEY=minioadmin
export EPOCH_S3_SECRET_KEY=changeme
export EPOCH_S3_BUCKET=epoch
export EPOCH_S3_SECURE=false

./epoch serve --addr :8080 --dsn 'epoch:epoch@tcp(127.0.0.1:3306)/epoch?parseTime=true'
```

## CLI

```bash
epoch serve                       # start HTTP server
epoch push NAME[:TAG]             # push a local cocoon snapshot
epoch pull NAME[:TAG]             # pull a snapshot or cloud image into cocoon
epoch get  NAME[:TAG]             # stream a cloud image's raw bytes to stdout
epoch ls   [NAME]                 # list repositories or tags
epoch inspect NAME[:TAG]          # show OCI manifest + classified kind
epoch tag SRC:OLD SRC:NEW         # re-tag a manifest in place
epoch rm  NAME:TAG                # remove a tag
```

### `epoch push <snapshot>` (snapshots only)

Stream a cocoon snapshot into the registry as an OCI artifact:

```bash
epoch push myvm                                   # push myvm:latest
epoch push myvm -t v1                             # specific tag
epoch push myvm -t v1 \
  --base-image ghcr.io/cocoonstack/cocoon/ubuntu:24.04
```

The `--base-image` flag is optional; when set it lands as the
`cocoonstack.snapshot.baseimage` annotation in the manifest. epoch never reads
`/var/lib/cocoon` directly — it shells out to `cocoon snapshot export -o -`
and uploads each tar entry as an OCI blob.

### `epoch pull <name>[:<tag>]`

Fetch an artifact, classify it, and pipe it into cocoon:

| Kind | What happens |
|---|---|
| `snapshot` (`vnd.cocoonstack.snapshot.v1+json`) | Reassemble tar → `cocoon snapshot import --name <name>` |
| `cloud-image` (`vnd.cocoonstack.os-image.v1+json`) | Concatenate disk layers in title order → `cocoon image import <name>` |
| `container-image` | Rejected — pull with `oras` / `crane` / `docker` and let cocoon's runtime use its built-in `cocoon image pull ghcr.io/...` path |

```bash
epoch pull myvm:v1                       # snapshot, imports as "myvm"
epoch pull myvm:v1 --name myvm-restored  # snapshot with overridden local name
epoch pull windows/win11:25h2            # cloud image, imports as "win11"
epoch pull windows/win11:25h2 --name win11-test
```

The `cocoon` binary must be on `$PATH`; override with `$EPOCH_COCOON_BINARY`.

### `epoch get <name>[:<tag>]` (cloud images only)

Stream the assembled disk bytes to stdout for piping. Snapshots cannot use
`epoch get` because they are not single contiguous artifacts — use
`epoch pull` instead.

```bash
epoch get windows/win11:25h2 | cocoon image import win11
ssh registry-host epoch get cocoon/ubuntu:24.04 | cocoon image import ubuntu
```

Progress goes to stderr so the data pipe stays clean.

## Examples

### Mirror a cocoonstack cloud image into epoch

```bash
# 1. Authenticate against epoch
echo $EPOCH_REGISTRY_TOKEN | crane auth login -u _token --password-stdin epoch.example

# 2. Mirror the upstream artifact (split-qcow2 windows image)
crane copy ghcr.io/cocoonstack/windows/win11:25h2 \
           epoch.example/windows/win11:25h2

# 3. Pull from epoch into cocoon on a node
epoch pull windows/win11:25h2

# Or, anonymously, via the public /dl/{name} short URL:
curl -fsSL https://epoch.example/dl/win11 | cocoon image import win11
```

### Push a cocoon snapshot to epoch

```bash
# On a cocoon node — cocoon CLI must be on $PATH
cocoon vm save myvm                     # creates snapshot in /var/lib/cocoon
epoch push myvm -t v1 \
  --base-image ghcr.io/cocoonstack/cocoon/ubuntu:24.04
```

### Pull and re-create a snapshot on another node

```bash
epoch pull myvm:v1 --name myvm-restored
cocoon vm clone --from myvm-restored --to myvm-clone
```

### Mirror a plain container image

`epoch pull` rejects container images (cocoon doesn't consume them). Use OCI
clients to mirror them through epoch:

```bash
crane copy ghcr.io/library/alpine:3.20 epoch.example/library/alpine:3.20
docker pull epoch.example/library/alpine:3.20
```

## Development

```bash
make build          # build binary
make test           # race-detected tests with coverage
make lint           # golangci-lint for linux and darwin
make fmt            # gofumpt and goimports
make deps           # tidy modules
make all            # full pipeline
make help           # show all targets
```

## Related Projects

| Project | Role |
|---|---|
| [cocoon-common](https://github.com/cocoonstack/cocoon-common) | Shared metadata, Kubernetes, and logging helpers |
| [cocoon-operator](https://github.com/cocoonstack/cocoon-operator) | CocoonSet and Hibernation CRDs |
| [cocoon-webhook](https://github.com/cocoonstack/cocoon-webhook) | Admission webhook for sticky scheduling |
| [vk-cocoon](https://github.com/cocoonstack/vk-cocoon) | Virtual kubelet provider managing VM lifecycle |

## License

[MIT](LICENSE)
