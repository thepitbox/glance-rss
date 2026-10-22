# The builder always runs natively and cross-compiles for the target platform,
# which avoids slow emulation when building arm images (e.g. for a Raspberry Pi)
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.1-alpine3.24 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev

RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
    go build -trimpath \
    -ldflags="-s -w -X github.com/glanceapp/glance/internal/glance.buildVersion=$VERSION" \
    -o glance .

# Glance is a static binary with timezone data embedded, it only needs CA certificates to fetch over HTTPS
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /app/glance /app/glance

WORKDIR /app
EXPOSE 8080/tcp
ENTRYPOINT ["/app/glance", "--config", "/app/config/glance.yml"]
