FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /epoch .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /epoch /usr/local/bin/epoch
EXPOSE 8080
ENTRYPOINT ["epoch"]
CMD ["serve"]
