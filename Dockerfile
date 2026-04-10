FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
ARG BUILTAT=unknown
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath \
      -ldflags="-s -w \
        -X github.com/cocoonstack/epoch/version.Version=${VERSION} \
        -X github.com/cocoonstack/epoch/version.Revision=${REVISION} \
        -X github.com/cocoonstack/epoch/version.BuiltAt=${BUILTAT}" \
      -o /out/epoch .

FROM alpine:3.21 AS runtime-deps
RUN apk add --no-cache ca-certificates

FROM busybox:stable-musl
COPY --from=runtime-deps /etc/ssl/certs/ /etc/ssl/certs/
COPY --from=build /out/epoch /usr/bin/epoch

# Pre-create the default upload spool directory so the server's
# resolveUploadDir picks it up instead of falling back to /tmp (which is
# tmpfs in most container runtimes and OOMs on multi-GiB pushes). The
# bundled epoch-server.yaml mounts an emptyDir over this path; standalone
# `docker run` users can either bind-mount real disk here or accept that
# the union FS layer carries the spool.
RUN mkdir -p /var/cache/epoch/uploads
ENV EPOCH_UPLOAD_DIR=/var/cache/epoch/uploads

EXPOSE 8080
ENTRYPOINT ["/usr/bin/epoch"]
CMD ["serve"]
