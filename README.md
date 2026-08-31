# Audiobookshelf Transcoder Proxy (abstp)

[![CI](https://github.com/underhax/audiobookshelf-transcoder-proxy/actions/workflows/ci.yml/badge.svg)](https://github.com/underhax/audiobookshelf-transcoder-proxy/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/underhax/audiobookshelf-transcoder-proxy?label=Release&include_prereleases)](https://github.com/underhax/audiobookshelf-transcoder-proxy/releases)
[![GitHub last commit](https://img.shields.io/github/last-commit/underhax/audiobookshelf-transcoder-proxy)](https://github.com/underhax/audiobookshelf-transcoder-proxy/commits/main)
[![GitHub issues](https://img.shields.io/github/issues/underhax/audiobookshelf-transcoder-proxy)](https://github.com/underhax/audiobookshelf-transcoder-proxy/issues)
[![GitHub repo size](https://img.shields.io/github/repo-size/underhax/audiobookshelf-transcoder-proxy)](https://github.com/underhax/audiobookshelf-transcoder-proxy)
[![Docker](https://github.com/underhax/audiobookshelf-transcoder-proxy/actions/workflows/build.yml/badge.svg)](https://github.com/underhax/audiobookshelf-transcoder-proxy/actions/workflows/build.yml)
[![Security Advisories](https://img.shields.io/github/security-advisories/underhax/audiobookshelf-transcoder-proxy)](https://github.com/underhax/audiobookshelf-transcoder-proxy/security/advisories)
[![Go Report](https://goreportcard.com/badge/github.com/underhax/audiobookshelf-transcoder-proxy)](https://goreportcard.com/report/github.com/underhax/audiobookshelf-transcoder-proxy)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Audiobookshelf Transcoder Proxy (`abstp`) is a lightweight, secure, and high-performance audio transcoding proxy service. It bridges [Audiobookshelf](https://www.audiobookshelf.org/) with external media players, smart speakers, and home automation systems.

Many external media players, smart speakers, and cast targets cannot natively adjust playback speed when streaming remote audio. `abstp` solves this by transcoding multi-track audiobooks and podcasts on-the-fly with **custom playback speed control** (from `0.5x` to `3.0x`) without pitch distortion, while maintaining real-time progress synchronization with your Audiobookshelf server.

The service is built with Go for maximum performance and low resource consumption, running alongside FFmpeg for real-time audio demuxing and transcoding.

---

## Features

- **Variable Playback Speed on Any Device**: Enables custom playback speeds (e.g. `0.5x` to `3.0x`) on smart speakers and cast targets that lack native speed controls, applying FFmpeg's `atempo` filter without pitch distortion.
- **On-the-Fly Concat & Transcoding**: Merges multi-track audiobooks and podcast episodes seamlessly into a continuous, single CBR AAC (64 kbps) audio stream without chapter pauses or playback interruption.
- **Single-Use Stream Tokens**: Generates cryptographically secure, single-use stream tokens with a configurable expiration TTL (default: `30s`). Tokens are invalidated upon connection, preventing unauthorized link sharing or URL hijacking by third parties.
- **Instant Pre-Buffering**: Delivers an immediate unthrottled burst of audio data (default: `10s`, configurable) directly to the player upon connection, eliminating startup latency and preventing playback stutter.
- **Paced Stream Rate Limiting**: Throttles ongoing audio delivery to match the target 64 kbps bitstream, reducing server memory usage and network congestion while maintaining rock-solid sync.
- **Two-Way Progress Synchronization**: Accurately tracks listened time accounting for playback speed and pre-buffering, syncing progress back to Audiobookshelf every 30 seconds and sending a final sync + session close on stop or disconnect.
- **Catalog & Artwork Proxy**: Exposes REST endpoints to query books, podcasts, episodes, and cached cover images.
- **Lightweight & Self-Contained**: Minimal CPU and memory footprint with zero external database requirements.
- **Secure Containerization**: Official Docker image runs as non-root (`65534:65534`) with a read-only filesystem, dropped Linux capabilities, and `no-new-privileges`.

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

   # 2.1 Paste your Audiobookshelf user API token:
   nano "${BASE_DIR}/secrets/abstp_abs_token.txt"

   # 2.2 Generate a strong secret key for proxy authentication:
   pwgen -s 64 1 > "${BASE_DIR}/secrets/abstp_api_key.txt"

   # 2.3 Restrict file permissions:
   chmod 600 "${BASE_DIR}/secrets/"*.txt
   ```

3. Edit `${BASE_DIR}/.env` with your configuration:
   ```bash
   nano "${BASE_DIR}/.env"
   ```

   > [!TIP]
   > **Network Access & Stream URLs:**
   > `abstp` automatically detects the incoming request protocol, host, and port to generate playback stream URLs.
   > - **Behind Reverse Proxy (Recommended):** Keep the default `127.0.0.1:8099:8099` in `docker-compose.yaml` and configure Nginx (see below).
   > - **Direct Local Network (LAN):** To allow other devices in your network (e.g. smart speakers) to connect directly without a reverse proxy, change `ports` in `docker-compose.yaml` to your server LAN IP (e.g. `- "192.168.1.50:8099:8099"`, replacing `192.168.1.50` with your actual server IP, or `- "8099:8099"` to bind to all interfaces).

4. Start the container:
   ```bash
   docker compose -f "${BASE_DIR}/docker-compose.yaml" up -d
   ```

To check service status and health, run:
```bash
docker compose -f "${BASE_DIR}/docker-compose.yaml" ps
```

To stop the service, run:
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
  -e ABSTP_EXTERNAL_URL="https://abstp.example.org" \
  ghcr.io/underhax/audiobookshelf-transcoder-proxy:latest
```

</details>

### Reverse Proxy Configuration (Nginx)

When deploying behind Nginx with SSL, configure `ABSTP_EXTERNAL_URL=https://abstp.example.org` and ensure proxy buffering is disabled to allow continuous real-time audio streaming:

<details>
<summary><b>View Nginx configuration example</b></summary>

```nginx
server {
    listen 443 ssl http2;
    server_name abstp.example.org;

    ssl_certificate /etc/letsencrypt/live/abstp.example.org/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/abstp.example.org/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8099;
        proxy_http_version 1.1;

        # Disable buffering for real-time throttled audio streams
        proxy_buffering off;
        proxy_request_buffering off;

        # Keep long-running audiobook streams alive
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;

        # Standard proxy headers
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

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

`abstp` provides built-in CLI commands and options for maintenance:

- **`help`, `-h`, `--help`**: Displays usage instructions, available flags, and environment variables:
  ```bash
  ./abstp help
  ```
- **`version`, `-v`, `--version`**: Prints the installed version of abstp:
  ```bash
  ./abstp version
  ```
- **`-healthcheck`**: Executes a lightweight HTTP health check against the local server (exiting with code 0 if healthy, 1 if unhealthy). Specifically designed for Docker container and Compose health checks:
  ```bash
  ./abstp -healthcheck
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

## License

This project is licensed under the [MIT License](LICENSE).
