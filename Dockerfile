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
        -X github.com/cocoonstack/epoch/version.VERSION=${VERSION} \
        -X github.com/cocoonstack/epoch/version.REVISION=${REVISION} \
        -X github.com/cocoonstack/epoch/version.BUILTAT=${BUILTAT}" \
      -o /out/epoch .

FROM alpine:3.21 AS runtime-deps
RUN apk add --no-cache ca-certificates

FROM busybox:stable-musl
COPY --from=runtime-deps /etc/ssl/certs/ /etc/ssl/certs/
COPY --from=build /out/epoch /usr/bin/epoch

EXPOSE 8080
ENTRYPOINT ["/usr/bin/epoch"]
CMD ["serve"]
