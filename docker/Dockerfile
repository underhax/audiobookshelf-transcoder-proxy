FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.24@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS builder
WORKDIR /app
COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -ldflags="-w -s -X main.Version=${VERSION}" -o abstp ./cmd/abstp

FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
# hadolint ignore=DL3018
RUN apk add --no-cache ffmpeg ca-certificates
WORKDIR /app
COPY --from=builder /app/abstp /app/abstp
USER 65534:65534
ENV ABSTP_LISTEN_ADDR=0.0.0.0:8099
ENV ABSTP_IN_DOCKER=true
EXPOSE 8099
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
  CMD ["/app/abstp", "--healthcheck"]
ENTRYPOINT ["/app/abstp"]
