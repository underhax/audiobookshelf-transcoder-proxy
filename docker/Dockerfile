FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine3.24@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS builder
WORKDIR /app
COPY go.mod ./
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -ldflags="-w -s -X main.Version=${VERSION}" -o abstp ./cmd/abstp

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
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
