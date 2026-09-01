# Audiobookshelf Transcoder Proxy (abstp)

[![CI](https://github.com/underhax/audiobookshelf-transcoder-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/underhax/audiobookshelf-transcoder-proxy/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/underhax/audiobookshelf-transcoder-proxy?label=Release&include_prereleases)](https://github.com/underhax/audiobookshelf-transcoder-proxy/releases)
[![GitHub last commit](https://img.shields.io/github/last-commit/underhax/audiobookshelf-transcoder-proxy)](https://github.com/underhax/audiobookshelf-transcoder-proxy/commits/main)
[![GitHub issues](https://img.shields.io/github/issues/underhax/audiobookshelf-transcoder-proxy)](https://github.com/underhax/audiobookshelf-transcoder-proxy/issues)
[![GitHub repo size](https://img.shields.io/github/repo-size/underhax/audiobookshelf-transcoder-proxy)](https://github.com/underhax/audiobookshelf-transcoder-proxy)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

`abstp` is a lightweight proxy service written in Go, utilizing FFmpeg for real-time audio processing.

It proxies library metadata, podcasts, and cover artwork from Audiobookshelf. The service serves on-the-fly transcoded continuous audio streams with custom playback speed control and two-way progress synchronization for smart speakers, media players, and home automation systems.

Official Docker images (with FFmpeg included) and standalone binaries are provided.

---

## Motivation

Native Audiobookshelf mobile apps provide rich playback controls and speed adjustment. However, when streaming to external smart speakers, network players, or cast targets, most devices face several limitations:

- **Lack of Speed Control**: Inability to adjust playback tempo for remote audio streams.
- **Multi-Track Transitions**: Unwanted pauses, gaps, or playback failures between audiobook chapters.
- **Progress Tracking Drift**: Inaccurate server progress synchronization during non-standard playback.

Audiobookshelf Transcoder Proxy resolves the problems listed above.

---

## Features

- **Playback Speed**: Dynamic tempo adjustment (`0.5x` to `3.0x`) without pitch distortion.
- **Audio Concat**: Seamless multi-track transcoding into a single continuous stream without pauses.
- **Single-Use Tokens**: Cryptographically secure stream URLs invalidated upon connection.
- **Smart Buffering**: Instant initial audio burst followed by paced rate-limited streaming.
- **Progress Sync**: Accurate real-time listening progress tracking adjusted for playback speed.
- **Metadata Proxy**: REST endpoints for browsing books, podcasts, and cached cover artwork.
- **Built-in Security**: Single-use stream tokens, automated CSP/security headers, concurrency limits, timing-attack resistant auth, path traversal guards, and more.
- **Hardened Container**: Non-root Docker image with a read-only filesystem, dropped capabilities, and privilege escalation protection.

---

## Installation

You can run `abstp` using Docker Compose or via a pre-compiled binary.

### Option 1: Docker / Docker Compose (Recommended)

You can deploy `abstp` using Docker Compose. A production-ready `docker-compose.yaml` is provided in the repository.

1. Define your base directory and download the configuration files:
   ```bash
   export BASE_DIR="/opt/abstp"
   mkdir -p "${BASE_DIR}"
   curl -sSL -o "${BASE_DIR}/docker-compose.yaml" https://raw.githubusercontent.com/underhax/audiobookshelf-transcoder-proxy/main/docker/docker-compose.yaml
   curl -sSL -o "${BASE_DIR}/.env" https://raw.githubusercontent.com/underhax/audiobookshelf-transcoder-proxy/main/docker/.env.example
   ```

2. Store your credentials securely using Docker Secrets:
   ```bash
   mkdir -p "${BASE_DIR}/secrets"

   # Paste your Audiobookshelf user API token:
   nano "${BASE_DIR}/secrets/abstp_abs_token.txt"

   # Generate a strong secret key for proxy authentication:
   pwgen -s 64 1 > "${BASE_DIR}/secrets/abstp_api_key.txt"

   # Restrict file permissions:
   chmod 400 "${BASE_DIR}/secrets/"*.txt && chown -R 65534:65534 "${BASE_DIR}/secrets/"
   ```

3. Edit `${BASE_DIR}/.env` with your configuration:
   ```bash
   nano "${BASE_DIR}/.env"
   ```

4. Run and manage the service:

   **Start the service:**
   ```bash
   docker compose -f "${BASE_DIR}/docker-compose.yaml" up -d
   ```

   **Check service status and health:**
   ```bash
   docker compose -f "${BASE_DIR}/docker-compose.yaml" ps
   ```

   **Stop and remove the service:**
   ```bash
   docker compose -f "${BASE_DIR}/docker-compose.yaml" down
   ```

<details>
<summary><b>View manual docker run command</b></summary>

```bash
docker run -d \
  --name abstp \
  --hostname abstp \
  -p 127.0.0.1:8099:8099 \
  --restart always \
  --user 65534:65534 \
  --ulimit nofile=65535:65535 \
  --cpus 0.5 \
  --memory 512m \
  --memory-reservation 64m \
  --security-opt no-new-privileges:true \
  --cap-drop ALL \
  --read-only \
  --tmpfs /tmp:mode=1777,noexec,nosuid \
  -v "${BASE_DIR}/secrets/abstp_abs_token.txt:/run/secrets/abstp_abs_token:ro" \
  -v "${BASE_DIR}/secrets/abstp_api_key.txt:/run/secrets/abstp_api_key:ro" \
  -e ABSTP_ABS_URL="https://abs.example.org" \
  ghcr.io/underhax/audiobookshelf-transcoder-proxy:latest
```

</details>

### Reverse Proxy Configuration (Nginx)

Example production-ready Nginx configuration for HTTPS reverse proxying and real-time streaming:

<details>
<summary><b>View Nginx configuration example</b></summary>

```nginx
server {
    listen 443 ssl http2;
    server_name abstp.example.org;

    # TLS protocols & recommended modern ciphers (Mozilla Intermediate profile)
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:DHE-RSA-AES128-GCM-SHA256:DHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;

    ssl_certificate /etc/letsencrypt/live/abstp.example.org/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/abstp.example.org/privkey.pem;

    # SSL session cache (enable if not already defined globally in http { ... } context):
    # ssl_session_cache shared:SSL:10m;
    # ssl_session_timeout 1d;

    # HTTP Strict Transport Security (HSTS) with subdomains
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;

    # Request body size limit for lightweight JSON payloads
    client_max_body_size 1m;

    # Security headers enforced automatically by the abstp backend:
    # - Content-Security-Policy: default-src 'none'; frame-ancestors 'none';
    # - X-Content-Type-Options: nosniff
    # - X-Frame-Options: DENY

    # Block access to hidden files (.git, .env, etc.)
    location ~ /\. {
        deny all;
    }

    location / {
        proxy_pass http://127.0.0.1:8099;
        proxy_http_version 1.1;
        proxy_set_header Connection "";

        # Real-time streaming without disk caching
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_max_temp_file_size 0;

        # Keep long-running audiobook streams alive
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;

        # Standard proxy headers
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $host;
    }
}
```

> **Note:** `abstp` implements built-in application-level concurrency controls (`ABSTP_MAX_CONNS` and `ABSTP_MAX_STREAMS`).
>
> If you wish to apply additional per-IP rate limiting at the reverse proxy layer, refer to the official [Nginx Rate Limiting documentation](https://docs.nginx.com/nginx/admin-guide/security-controls/controlling-access-proxied-http/).

</details>

---

### Option 2: Binary Release

1. Download the latest archive for your operating system and architecture from the [Releases](https://github.com/underhax/audiobookshelf-transcoder-proxy/releases) page.
2. Ensure `ffmpeg` is installed and available in your system `PATH`.
3. Extract the archive. The binary is already executable, but we recommend restricting permissions for better security (Linux/macOS):
   ```bash
   chmod 500 abstp
   ```
4. Run the application:
   ```bash
   ABSTP_ABS_URL="https://abs.example.org" \
   ABSTP_ABS_TOKEN_FILE="/path/to/abs_token.txt" \
   ABSTP_API_KEY_FILE="/path/to/api_key.txt" \
   ./abstp
   ```

#### CLI Commands

Audiobookshelf Transcoder Proxy provides built-in CLI commands and options for maintenance:

**`--help`** (`-h`): Displays usage instructions, available flags, and environment variables:
```bash
./abstp --help
```

**`--version`** (`-v`): Prints the installed version of abstp:
```bash
./abstp --version
```

**`--healthcheck`**: Executes a lightweight HTTP health check against the local server:
```bash
./abstp --healthcheck
```

#### Configuration

`abstp` is configured using the following environment variables:

- `ABSTP_ABS_URL`: **(Required)** Base URL of your Audiobookshelf server (e.g. `https://abs.example.org` or `http://192.168.1.100:13378`).
- `ABSTP_ABS_TOKEN`: **(Required)** Audiobookshelf user API token or bearer token (or provide via `ABSTP_ABS_TOKEN_FILE`).
- `ABSTP_ABS_TOKEN_FILE`: Path to a file containing the Audiobookshelf token (supports Docker / Kubernetes Secrets or default `/run/secrets/abstp_abs_token`).
- `ABSTP_API_KEY`: **(Required)** Secret Bearer key required by clients to authenticate requests against this proxy (or provide via `ABSTP_API_KEY_FILE`).
- `ABSTP_API_KEY_FILE`: Path to a file containing the proxy authentication secret key (supports Docker / Kubernetes Secrets or default `/run/secrets/abstp_api_key`).
- `ABSTP_LISTEN_ADDR`: Server listen address and port in `host:port` format (default: `127.0.0.1:8099`). For security reasons, the port must be within `1025` to `65535`. *(Note: When using Docker, set to `0.0.0.0:8099`)*.
- `ABSTP_EXTERNAL_URL`: External base URL returned in playback stream links (default: automatically inferred from incoming HTTP requests). Set only if you wish to enforce a fixed stream URL or domain (e.g. `https://abstp.example.org`).
- `ABSTP_FFMPEG_PATH`: Path to the `ffmpeg` binary (default: `ffmpeg`).
- `ABSTP_TOKEN_TTL`: Validity duration for single-use playback stream tokens (default: `30s`).
- `ABSTP_BUFFER_DURATION`: Initial stream burst buffer duration sent immediately to prime client buffers (default: `10s`, minimum: `5s`).
- `ABSTP_MAX_CONNS`: Global maximum concurrent incoming HTTP connections (default: `100`, allowed: `50` to `1000`).
- `ABSTP_MAX_STREAMS`: Maximum concurrent active FFmpeg transcoding streams (default: `5`, allowed: `1` to `20`).
- `ABSTP_IN_DOCKER`: Set to `true` when running in Docker to suppress terminal interactivity messages (default: `false`).

<details>
<summary><b>View execution examples</b></summary>

**1. Basic local execution with secret files:**
```bash
ABSTP_ABS_URL="https://abs.example.org" \
ABSTP_ABS_TOKEN_FILE="/path/to/abs_token.txt" \
ABSTP_API_KEY_FILE="/path/to/api_key.txt" \
./abstp
```

**2. Custom port, external network URL, and custom buffer duration:**
```bash
ABSTP_ABS_URL="https://abs.example.org" \
ABSTP_ABS_TOKEN_FILE="/path/to/abs_token.txt" \
ABSTP_API_KEY_FILE="/path/to/api_key.txt" \
ABSTP_LISTEN_ADDR="0.0.0.0:9099" \
ABSTP_EXTERNAL_URL="http://192.168.1.50:9099" \
ABSTP_BUFFER_DURATION="15s" \
./abstp
```

</details>

---

## Quick Usage Example

### 1. Browse Catalog
```bash
curl -s http://127.0.0.1:8099/api/proxy/books \
  -H "Authorization: Bearer your_proxy_secret_key"
```

### 2. Start a Playback Session
```bash
curl -s -X POST http://127.0.0.1:8099/api/proxy/session/start \
  -H "Authorization: Bearer your_proxy_secret_key" \
  -H "Content-Type: application/json" \
  -d '{
    "item_id": "book-item-id-here",
    "speed": 1.25
  }'
```

Sample JSON response:
```json
{
  "session_id": "sess_019234ab89cd",
  "stream_url": "http://127.0.0.1:8099/stream/sess_019234ab89cd.aac?token=67a8b9c0d1e2...",
  "current_time": 1250.0,
  "duration": 36000.0
}
```

### 3. Play the Stream
Open the returned `stream_url` directly in any web browser (Chrome, Firefox, Safari, Edge) or pass it to your media player / smart speaker:
```
http://127.0.0.1:8099/stream/sess_019234ab89cd.aac?token=67a8b9c0d1e2...
```

### 4. Stop the Session
```bash
curl -s -X POST http://127.0.0.1:8099/api/proxy/session/stop \
  -H "Authorization: Bearer your_proxy_secret_key" \
  -H "Content-Type: application/json" \
  -d '{
    "session_id": "sess_019234ab89cd"
  }'
```

---

## Development

For instructions on how to set up the development environment, build the project from source, or run the test suite, please refer to [DEVELOPMENT](DEVELOPMENT.md).

---

## Acknowledgments

- [Audiobookshelf](https://www.audiobookshelf.org/) — Self-hosted audiobook and podcast server.
- [FFmpeg](https://ffmpeg.org/) — Multimedia framework for audio processing and transcoding.
