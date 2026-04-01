# Epoch

Epoch is a snapshot registry for [Cocoon](https://github.com/cocoonstack/cocoon) MicroVMs. It stores versioned manifests and content-addressed blobs in any S3-compatible object store, exposes an OCI-style `/v2/` API, and ships a small web UI for browsing repositories and managing access tokens.

## Features

- **Content-addressed storage** -- blobs are de-duplicated by SHA-256 digest
- **OCI Distribution API** -- `/v2/` push and pull, compatible with standard registry tooling
- **MySQL metadata index** -- queryable catalog for the web UI and control API
- **SSO login** -- optional Google OAuth or generic OIDC for the web UI
- **Token management** -- create and revoke bearer tokens from the dashboard
- **vk-cocoon integration** -- `registry.NewPuller(...)` pulls snapshots on demand before VM creation

## Installation

### Download from GitHub Releases

Download the latest pre-built binary from the [GitHub Releases](https://github.com/cocoonstack/epoch/releases) page:

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
make build
```

The `epoch` binary will be created in the current directory.

## Quick start

Start local dependencies:

```bash
cd deploy
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

Push and inspect a snapshot:

```bash
./epoch push ubuntu-dev --tag latest
./epoch ls
./epoch inspect ubuntu-dev:latest
```

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

## Object storage configuration

| Variable | Description |
|---|---|
| `EPOCH_S3_ENDPOINT` | S3 endpoint (with or without scheme) |
| `EPOCH_S3_ACCESS_KEY` | Access key |
| `EPOCH_S3_SECRET_KEY` | Secret key |
| `EPOCH_S3_BUCKET` | Bucket name |
| `EPOCH_S3_REGION` | Region (optional) |
| `EPOCH_S3_SECURE` | `true` / `false`; inferred from scheme if omitted |
| `EPOCH_S3_PREFIX` | Key prefix (default `epoch/`) |
| `EPOCH_S3_ENV_FILE` | Env file path (default `~/.config/epoch/s3.env`) |

## Authentication

**Registry clients** (`/v2/`):
- Bearer token from `EPOCH_REGISTRY_TOKEN` or tokens created via the UI
- Tokens are validated by SHA-256 hash against MySQL

**Web UI / control API**:
- Disabled by default (open access)
- Set `SSO_PROVIDER=google` or `SSO_PROVIDER=oidc` to enable session-based login
- See [deploy/epoch-server.yaml](deploy/epoch-server.yaml) for the full list of SSO variables

## Deployment

| Path | Purpose |
|---|---|
| `deploy/docker-compose.yaml` | Local MySQL + MinIO |
| `deploy/epoch-server.yaml` | Kubernetes Deployment template |
| `deploy/Dockerfile` | Container image build |
| `deploy/epoch-server.service` | systemd unit file |

## Development

```bash
make deps          # tidy modules
make fmt           # gofumpt + goimports
make lint          # golangci-lint
make test          # race-detected tests with coverage
make build         # stripped binary
make all           # full pipeline
```

Run `make help` for the complete target list.

## License

MIT -- see [LICENSE](LICENSE).
