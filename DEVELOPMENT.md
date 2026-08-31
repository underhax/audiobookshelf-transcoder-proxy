# abstp Development Guide

## Prerequisites

Ensure the following dependencies are installed prior to development and testing:

### Development & CI (Required)
- Git
- Go 1.27+
- GNU Make
- Docker (required for building the container image)
- FFmpeg (required for audio transcoding runtime)
- golangci-lint (required for Go code linting)
- trivy (required for container and filesystem security scanning)
- govulncheck (required for Go vulnerability checking)
- hadolint (required for Dockerfile linting)

### Local Manual Testing
- curl (required for executing HTTP API requests)
- Web Browser (Chrome, Firefox, Edge, Safari, etc. for verifying the live audio stream)
- Python 3 or jq (optional, for automated JSON response parsing into shell variables)
- ffplay or mpv (optional, alternative CLI players for verifying the stream)

## Tooling Installation

Install the required Go linter, vulnerability checker, and security scanners using any convenient method for your platform. These tools are utilized in our CI pipeline and are required for local code validation:
- **Go 1.27+**: [https://go.dev/dl/](https://go.dev/dl/)
- **FFmpeg**: [https://ffmpeg.org/download.html](https://ffmpeg.org/download.html) (also includes `ffplay`)
- **golangci-lint**: [https://golangci-lint.run/welcome/install/](https://golangci-lint.run/welcome/install/)
- **trivy**: [https://trivy.dev/docs/latest/getting-started/](https://trivy.dev/docs/latest/getting-started/)
- **govulncheck**: `go install golang.org/x/vuln/cmd/govulncheck@latest`
- **hadolint**: [https://github.com/hadolint/hadolint#install](https://github.com/hadolint/hadolint#install)
- **curl**: [https://curl.se/download.html](https://curl.se/download.html)

Optional tools for manual testing:
- **mpv** *(CLI player)*: [https://mpv.io/installation/](https://mpv.io/installation/)
- **Python 3**: [https://www.python.org/downloads/](https://www.python.org/downloads/)
- **jq**: [https://jqlang.github.io/jq/download/](https://jqlang.github.io/jq/download/)

## Code Validation & Testing

The project enforces strict code quality standards utilizing multiple tools (`gofmt`, `golangci-lint`, `hadolint`, `trivy`, `govulncheck`). All validation steps are centralized in the `Makefile`.

1. **Linting & Dry Compilation:**
   ```sh
   make check
   ```

2. **Unit Tests & Race Detection:**
   ```sh
   make test
   ```

3. **Vulnerability & Security Scanning:**
   ```sh
   make vulncheck
   make trivy
   ```

4. **Docker Validation:**
   ```sh
   make docker-lint
   ```
   If you have Docker Compose installed locally, you can also manually validate the syntax of compose files:
   ```sh
   docker compose -f docker/docker-compose.yaml config -q
   docker compose -f docker/docker-compose.dev.yaml config -q
   ```

5. **Code Coverage:**
   ```sh
   make coverage
   ```

6. **Complete Project Verification:**
   Prior to submitting any pull request, execute the complete validation and test suite:
   ```sh
   make verify
   ```

## Build Workflow

1. **Compile the local application:**
   ```sh
   make build
   ```
   This command builds the standalone `abstp` binary in the repository root.

2. **Compile with a specific version flag (Optional):**
   ```sh
   make build VERSION=1.0.0
   ```

3. **Cross-compile for other platforms (Optional):**
   You can compile the application for any of the officially supported release targets by specifying the standard Go environment variables (`GOOS` and `GOARCH`):

   **Linux:**
   ```sh
   GOOS=linux GOARCH=amd64 make build
   GOOS=linux GOARCH=arm64 make build
   ```

   **Windows:**
   ```sh
   GOOS=windows GOARCH=amd64 make build
   GOOS=windows GOARCH=arm64 make build
   ```

   **macOS (Darwin):**
   ```sh
   GOOS=darwin GOARCH=amd64 make build
   GOOS=darwin GOARCH=arm64 make build
   ```

4. **Run via Docker Compose (Development):**
   ```sh
   docker compose -f docker/docker-compose.dev.yaml up --build
   ```

## Environment Configuration

Configure the environment variables in your terminal before launching `abstp`:

```bash
# 1. Target Audiobookshelf instance (without trailing slash)
ABSTP_ABS_URL="https://abs.example.org"

# 2. Audiobookshelf API token
ABSTP_ABS_TOKEN="your_abs_token_here"

# 3. Secret API key used by Home Assistant / clients to access this proxy
ABSTP_API_KEY="your_proxy_secret_key_here"

# 4. Address and port for proxy server (default: 127.0.0.1:8099)
ABSTP_LISTEN_ADDR="127.0.0.1:8099"

# 5. External URL reachable by players (smart speakers, mpv)
ABSTP_EXTERNAL_URL="http://127.0.0.1:8099"

# 6. Stream token TTL (default: 30s for production; increase to 5m for manual testing if desired)
ABSTP_TOKEN_TTL="30s"

# 7. Initial stream buffer duration (default: 10s; minimum: 5s)
ABSTP_BUFFER_DURATION="10s"

# 8. Running inside Docker (default: false, automatically true in Docker container)
ABSTP_IN_DOCKER="false"

# 9. Local proxy base URL for testing commands
ABSTP_PROXY_URL="http://127.0.0.1:8099"
```

---

## Local Testing Workflow

### Step 1: Start the Proxy Service

Launch `abstp` in your first terminal (passing variables or using your configuration):

```bash
ABSTP_ABS_URL="${ABSTP_ABS_URL}" \
ABSTP_ABS_TOKEN="${ABSTP_ABS_TOKEN}" \
ABSTP_API_KEY="${ABSTP_API_KEY}" \
./abstp
```

Verify service liveness:

```bash
curl -i "${ABSTP_PROXY_URL}/health"
```

Expected response: `HTTP/1.1 200 OK` with body `{"status":"ok"}`.

---

### Step 2: Browse Catalog (Books & Podcasts)

The proxy provides media catalog endpoints designed for Home Assistant Media Browser:

#### List Audiobooks:
```bash
curl -s "${ABSTP_PROXY_URL}/api/proxy/books" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  | python3 -m json.tool --no-ensure-ascii
```

<details>
<summary>Alternative: using jq</summary>

```bash
curl -s "${ABSTP_PROXY_URL}/api/proxy/books" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  | jq .
```

</details>

Sample output:
```json
[
  {
    "id": "36f7f1b1-7b87-4fc7-8eb3-4880749a0aa1",
    "title": "Harry Potter and the Sorcerer's Stone",
    "author": "J.K. Rowling",
    "mediaType": "book",
    "duration": 34200.0,
    "progress": 1450.5,
    "coverUrl": "/api/proxy/covers/36f7f1b1-7b87-4fc7-8eb3-4880749a0aa1"
  }
]
```

#### List Podcasts:
```bash
curl -s "${ABSTP_PROXY_URL}/api/proxy/podcasts" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  | python3 -m json.tool --no-ensure-ascii
```

<details>
<summary>Alternative: using jq</summary>

```bash
curl -s "${ABSTP_PROXY_URL}/api/proxy/podcasts" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  | jq .
```

</details>

Sample output:
```json
[
  {
    "id": "b20b0609-3e23-4083-82fd-123cab2e57e9",
    "title": "Unpacking the Track",
    "mediaType": "podcast",
    "duration": 0.0,
    "progress": 0.0,
    "coverUrl": "/api/proxy/covers/b20b0609-3e23-4083-82fd-123cab2e57e9"
  }
]
```

#### List Podcast Episodes:
Set the podcast ID from the list above and query its episodes:

```bash
ABSTP_PODCAST_ID="<podcast_id_from_podcasts_list>"
```

```bash
curl -s "${ABSTP_PROXY_URL}/api/proxy/podcasts/${ABSTP_PODCAST_ID}/episodes" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  | python3 -m json.tool --no-ensure-ascii
```

<details>
<summary>Alternative: using jq</summary>

```bash
curl -s "${ABSTP_PROXY_URL}/api/proxy/podcasts/${ABSTP_PODCAST_ID}/episodes" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  | jq .
```

</details>

Sample output:
```json
[
  {
    "id": "ep_lh6ko39pumnrma3dhv",
    "title": "Why Enough is Rebellion",
    "season": "1",
    "episode": "9",
    "publishedAt": "2026-04-08T00:00:00Z",
    "duration": 1980.0,
    "progress": 420.0
  }
]
```

#### View Cover Artwork:
Open in browser or test via curl:

**Audiobook Cover Artwork:**
```bash
curl -i "${ABSTP_PROXY_URL}/api/proxy/covers/${ABSTP_ITEM_ID}"
```

**Podcast Cover Artwork:**
```bash
curl -i "${ABSTP_PROXY_URL}/api/proxy/covers/${ABSTP_PODCAST_ID}"
```

Returns artwork with `Cache-Control: public, max-age=86400` so clients cache images locally.

---

### Step 3: Select Item & Start Playback Session

Choose either an **Audiobook** or a **Podcast Episode** to begin playback:

#### Option A: Audiobook

1. Set your audiobook item ID and desired speed:
```bash
ABSTP_ITEM_ID="<book_item_id_from_catalog>"
ABSTP_SPEED=1.25
```

2. Start the playback session:

**Standard curl (manual variable assignment):**
```bash
curl -i -X POST "${ABSTP_PROXY_URL}/api/proxy/session/start" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "item_id": "'"${ABSTP_ITEM_ID}"'",
    "speed": '"${ABSTP_SPEED}"'
  }'
```

Copy the returned `session_id` and `stream_url` from the response body:
```bash
ABSTP_SESSION_ID="<session_id_from_response>"
ABSTP_STREAM_URL="<stream_url_from_response>"
```

<details>
<summary><b>Alternative: Automated variable extraction for Audiobook (Python 3 or jq)</b></summary>

**Using Python 3:**
```bash
RESP=$(curl -s -X POST "${ABSTP_PROXY_URL}/api/proxy/session/start" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"item_id":"'"${ABSTP_ITEM_ID}"'","speed":'"${ABSTP_SPEED}"'}')

ABSTP_SESSION_ID=$(echo "$RESP" | python3 -c "import sys, json; print(json.load(sys.stdin).get('session_id', ''))")
ABSTP_STREAM_URL=$(echo "$RESP" | python3 -c "import sys, json; print(json.load(sys.stdin).get('stream_url', ''))")

echo "Active Session: ${ABSTP_SESSION_ID}"
echo "Stream URL:     ${ABSTP_STREAM_URL}"
```

**Using jq:**
```bash
RESP=$(curl -s -X POST "${ABSTP_PROXY_URL}/api/proxy/session/start" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"item_id":"'"${ABSTP_ITEM_ID}"'","speed":'"${ABSTP_SPEED}"'}')

ABSTP_SESSION_ID=$(echo "$RESP" | jq -r .session_id)
ABSTP_STREAM_URL=$(echo "$RESP" | jq -r .stream_url)

echo "Active Session: ${ABSTP_SESSION_ID}"
echo "Stream URL:     ${ABSTP_STREAM_URL}"
```

</details>

---

#### Option B: Podcast Episode

1. Set your podcast episode ID and desired speed (`ABSTP_PODCAST_ID` was set in Step 2):
```bash
ABSTP_EPISODE_ID="<episode_id_from_episodes_list>"
ABSTP_SPEED=1.25
```

2. Start the playback session:

**Standard curl (manual variable assignment):**
```bash
curl -i -X POST "${ABSTP_PROXY_URL}/api/proxy/session/start" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "item_id": "'"${ABSTP_PODCAST_ID}"'",
    "episode_id": "'"${ABSTP_EPISODE_ID}"'",
    "speed": '"${ABSTP_SPEED}"'
  }'
```

Copy the returned `session_id` and `stream_url` from the response body:
```bash
ABSTP_SESSION_ID="<session_id_from_response>"
ABSTP_STREAM_URL="<stream_url_from_response>"
```

<details>
<summary><b>Alternative: Automated variable extraction for Podcast (Python 3 or jq)</b></summary>

**Using Python 3:**
```bash
RESP=$(curl -s -X POST "${ABSTP_PROXY_URL}/api/proxy/session/start" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"item_id":"'"${ABSTP_PODCAST_ID}"'","episode_id":"'"${ABSTP_EPISODE_ID}"'","speed":'"${ABSTP_SPEED}"'}')

ABSTP_SESSION_ID=$(echo "$RESP" | python3 -c "import sys, json; print(json.load(sys.stdin).get('session_id', ''))")
ABSTP_STREAM_URL=$(echo "$RESP" | python3 -c "import sys, json; print(json.load(sys.stdin).get('stream_url', ''))")

echo "Active Session: ${ABSTP_SESSION_ID}"
echo "Stream URL:     ${ABSTP_STREAM_URL}"
```

**Using jq:**
```bash
RESP=$(curl -s -X POST "${ABSTP_PROXY_URL}/api/proxy/session/start" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{"item_id":"'"${ABSTP_PODCAST_ID}"'","episode_id":"'"${ABSTP_EPISODE_ID}"'","speed":'"${ABSTP_SPEED}"'}')

ABSTP_SESSION_ID=$(echo "$RESP" | jq -r .session_id)
ABSTP_STREAM_URL=$(echo "$RESP" | jq -r .stream_url)

echo "Active Session: ${ABSTP_SESSION_ID}"
echo "Stream URL:     ${ABSTP_STREAM_URL}"
```

</details>

---

### Step 4: Stream Audio

Play the live transcoded stream:

**Primary Method (Web Browser):**
Simply open the `ABSTP_STREAM_URL` in any modern web browser (Chrome, Firefox, Edge, Safari) to test the audio output locally.

<details>
<summary><b>Alternative: CLI Players (ffplay / mpv)</b></summary>

If you prefer using command-line players:

```bash
# ffplay is typically included with FFmpeg installations
ffplay -nodisp -autoexit "${ABSTP_STREAM_URL}"
```

```bash
# mpv (--no-cache disables deep network pre-buffering for immediate responsiveness)
mpv --no-cache "${ABSTP_STREAM_URL}"
```

</details>

#### Streaming Behavior:
1. **Initial Burst**: Streams 80,000 bytes (~10s of audio, configurable via `ABSTP_BUFFER_DURATION`) immediately to fill player buffer.
2. **Throttling**: Enforces steady 64 kbps (8,000 bytes/sec) AAC bitstream.
3. **Speed Filter**: `atempo` filter adjusts speed without altering audio pitch.
4. **Auto-Progress Sync**: Every 30 seconds, progress is synchronized to Audiobookshelf.

---

### Step 5: Stop Playback & Verify Progress

Playback can be cleanly terminated in two ways:

#### Option A: Disconnect the Player (`Ctrl+C` in `ffplay` or `mpv`)
When the media player disconnects:
* `abstp` intercepts the connection drop.
* Immediately terminates the FFmpeg process.
* Performs a final progress sync to Audiobookshelf.
* Closes the session on Audiobookshelf and deletes it from local memory.

#### Option B: Explicit Stop API Call
In another terminal window:

```bash
curl -i -X POST "${ABSTP_PROXY_URL}/api/proxy/session/stop" \
  -H "Authorization: Bearer ${ABSTP_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "session_id": "'"${ABSTP_SESSION_ID}"'"
  }'
```

Expected response: `HTTP/1.1 200 OK` with body `{"status":"stopped"}`.

#### Verifying Persistence:
Open the Audiobookshelf web interface (`${ABSTP_ABS_URL}`). Observe that:
1. The book's progress bar has advanced accurately by the listened duration.
2. The active session in Audiobookshelf has closed (no lingering or orphaned sessions).
