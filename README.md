# epoch

Snapshot registry for [Cocoon](https://github.com/cocoonstack/cocoon) MicroVMs. It stores versioned manifests and content-addressed blobs in any S3-compatible object store, exposes an OCI-style `/v2/` API, and ships a small web UI for browsing repositories and managing access tokens.

## Overview

- **Content-addressed storage** -- blobs are de-duplicated by SHA-256 digest
- **OCI Distribution API** -- `/v2/` push and pull, compatible with standard registry tooling
- **Streaming export** -- `epoch get` streams snapshots or cloud images to stdout for direct piping into `cocoon snapshot import` or `cocoon image import`
- **MySQL metadata index** -- queryable catalog for the web UI and control API
- **SSO login** -- optional Google OAuth or generic OIDC for the web UI
- **Token management** -- create and revoke bearer tokens from the dashboard
- **vk-cocoon integration** -- `registry.NewPuller(...)` pulls snapshots on demand before VM creation

## Architecture

```text
              vk-cocoon / epoch CLI
                       |
                 registry package
                 /             \
            S3 object store   Epoch HTTP server
                                /      |      \
                          /v2/ API   /api/   web UI
                                      |
                                    MySQL
```

Object layout in the bucket:

```text
epoch/
  catalog.json
  manifests/<repo>/<tag>.json
  blobs/sha256/<digest>
```

## Installation

### Download

Download a pre-built binary from [GitHub Releases](https://github.com/cocoonstack/epoch/releases):

```bash
# Linux (amd64)
curl -fSL -o epoch https://github.com/cocoonstack/epoch/releases/latest/download/epoch-linux-amd64
chmod +x epoch
sudo mv epoch /usr/local/bin/

# Linux (arm64)
curl -fSL -o epoch https://github.com/cocoonstack/epoch/releases/latest/download/epoch-linux-arm64
chmod +x epoch
sudo mv epoch /usr/local/bin/

# macOS (Apple Silicon)
curl -fSL -o epoch https://github.com/cocoonstack/epoch/releases/latest/download/epoch-darwin-arm64
chmod +x epoch
sudo mv epoch /usr/local/bin/
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

### Deployment files

| Path | Purpose |
|---|---|
| `docker-compose.yaml` | Local MySQL and MinIO |
| `epoch-server.yaml` | Kubernetes Deployment template |
| `Dockerfile` | Container image build |
| `epoch-server.service` | systemd unit file |

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

./epoch serve --addr :4300 --dsn 'epoch:epoch@tcp(127.0.0.1:3306)/epoch?parseTime=true'
```

## CLI

```bash
epoch serve           # start HTTP server
epoch push NAME:TAG   # push a local cocoon snapshot to the registry
epoch pull NAME:TAG   # pull a snapshot and write to cocoon's local storage
epoch get  NAME:TAG   # stream a snapshot or cloud image to stdout (see below)
epoch ls [NAME]       # list repositories or tags
epoch inspect NAME:TAG # show manifest details (JSON)
epoch tag SRC DST     # create a new tag from an existing manifest
epoch rm  NAME:TAG    # remove a tag
```

### Streaming with `epoch get`

`epoch get` auto-detects the artifact type and streams the appropriate format to stdout:

- **Snapshots** → gzip-compressed tar archive (compatible with `cocoon snapshot import`)
- **Cloud images** → gzip-compressed qcow2 data (compatible with `cocoon image import`)

```bash
# Pull a snapshot directly into cocoon
epoch get myvm:v1 | cocoon snapshot import --name myvm

# Pull a cloud image directly into cocoon
epoch get ubuntu-base:latest | cocoon image import ubuntu-base

# Transfer across hosts
ssh registry-host epoch get myvm:v1 | cocoon snapshot import --name myvm
```

Progress output goes to stderr so it does not interfere with the data stream.

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
