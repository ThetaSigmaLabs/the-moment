# Deployment

This guide covers all ways to run The Moment: Docker without cloning (fastest), Docker with the Makefile (recommended for ongoing use), pre-built binary (native), and VS Code dev mode. It also documents every environment variable.

## Before you start

- Spoolman must be reachable before The Moment starts. On first run it seeds the Spoolman URL into the database; after that the UI controls it.
- All run modes use the same env vars. The difference is where those vars are set.

---

## Option 0 — Docker without cloning (fastest)

No git, no build. Three commands and you are running.

```bash
curl -O https://raw.githubusercontent.com/ThetaSigmaLabs/the-moment/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/ThetaSigmaLabs/the-moment/main/.env.example && cp .env.example .env
docker compose up -d
```

Open `http://<server-ip>:5000`. Go to Settings → Printers → Add Printer.

Edit `.env` to change ports, data paths, or timezone before starting. The defaults work on any single-machine install.

### Update

```bash
docker compose pull && docker compose up -d
```

### Stop / Start

```bash
docker compose down
docker compose up -d
```

---

## Option 1 — Docker with Makefile (recommended for ongoing use)

Clone the repo to get the Makefile, which adds backup, restore, and dev workflow targets.

### 1. Clone

```bash
git clone https://github.com/ThetaSigmaLabs/the-moment.git
cd the-moment
```

### 2. Configure

```bash
make setup
```

`make setup` copies `.env.example` to `.env` (if not already present) and creates the data directories. Then edit `.env` to review ports and timezone.

`.env` is loaded by both `docker-compose.yml` and the `Makefile`. It is git-ignored — never commit it.

### 3. Start

```bash
make up
```

`make up` creates the data directories before starting, which prevents Docker from creating them as root-owned.

### 4. Check it's running

```bash
make ps          # container status
make logs        # tail combined logs (Ctrl-C to stop)
make open        # open the web UI in the default browser
```

Open `http://<server-ip>:5000` (or your `THE_MOMENT_PORT`) to reach the web UI.

### Makefile reference

Run `make help` to see all targets. Key ones:

| Target | What it does |
|---|---|
| `make setup` | First-time: copy `.env.example` → `.env`, create data dirs |
| `make up` | Create data dirs and start all services |
| `make down` | Stop all services |
| `make logs` | Tail live logs from all services (Ctrl-C to stop) |
| `make update` | Pull latest images and restart |
| `make ps` | Show running containers and status |
| `make open` | Open the web UI in the default browser |
| `make backup` | Stop, archive all data + config, restart |
| `make restore BACKUP=<path>` | Restore from a backup archive |
| `make test-unit` | Run unit tests (fast, no external deps) |
| `make test-integration` | Run integration tests (spins up in-process DB) |
| `make lint` | Run go vet + staticcheck |

### Update

```bash
make update
# equivalent to: docker compose pull && docker compose up -d
```

### Stop

```bash
make down
```

### Backup and restore

```bash
make backup                                                        # archives all data dirs + .env to BACKUP_DIR
make restore BACKUP=./backups/backup-20260101-120000.tar.gz
```

`make backup` stops the services, archives the data, and restarts. Archives are named `backup-YYYYMMDD-HHMMSS.tar.gz`.

_Source: `Makefile`_

---

## Option 2 — Pre-built binary (native)

Use this when you want to run The Moment directly on the host without Docker, or on a platform where containers are impractical.

### 1. Download the binary

Download the latest release for your platform from the [Releases page](https://github.com/ThetaSigmaLabs/the-moment/releases):

| Platform | File |
|---|---|
| Linux amd64 | `the-moment-linux-amd64` |
| Linux arm64 (e.g. Raspberry Pi, Odroid) | `the-moment-linux-arm64` |
| macOS Intel | `the-moment-darwin-amd64` |
| macOS Apple Silicon | `the-moment-darwin-arm64` |
| Windows | `the-moment-windows-amd64.exe` |

```bash
chmod +x the-moment-linux-arm64
mv the-moment-linux-arm64 the-moment
```

### 2. Start Spoolman

Spoolman must be running and reachable. Run it separately or use the Docker compose file for Spoolman only:

```bash
docker compose up -d spoolman
```

### 3. Set environment variables

Copy `.env.example` to `.env` and adjust. Then export the variables before starting:

```bash
set -a && source .env && set +a
./the-moment
```

Or pass them inline:

```bash
THE_MOMENT_DB_PATH=./data/db \
THE_MOMENT_GCODE_PATH=./data/gcode \
THE_MOMENT_UPLOADS_PATH=./data/uploads \
./the-moment
```

Without any env vars the binary uses these defaults (resolved relative to the working directory):

| Path | Default |
|---|---|
| DB directory | `./the-moment-data/db/` |
| Gcode directory | `./the-moment-data/gcode/` |
| Uploads directory | `./the-moment-data/uploads/` |
| Backup store | `./backups/` |
| Port | `5000` |

The data directories are created automatically on first run. The backup store is created on the first backup.

### 4. Open the UI

`http://localhost:5000` (or your `THE_MOMENT_PORT`).

### Build from source

```bash
git clone https://github.com/ThetaSigmaLabs/the-moment.git
cd the-moment
go mod download
go build -o the-moment .
./the-moment
```

Go 1.24 or higher is required.

---

## Option 3 — VS Code dev mode

This mode runs the binary directly from VS Code with the Go debugger attached. It is intended for development, not production.

### Prerequisites

- Go 1.24+ installed
- VS Code with the Go extension
- Spoolman running (the launch tasks handle this)

### Data layout

The dev launch configs override all three data paths to `.dev/` inside the workspace:

| What | Path |
|---|---|
| Database | `<workspace>/.dev/db/the-moment.db` |
| Gcode files | `<workspace>/.dev/gcode/` |
| Uploaded files | `<workspace>/.dev/uploads/` |

`.dev/` is git-ignored. Create it with the subdirectories before the first run:

```bash
mkdir -p .dev/db .dev/gcode .dev/uploads
```

If you have an existing dev DB at `.dev/the-moment.db` from before this layout was introduced, move it:

```bash
mkdir -p .dev/db && mv .dev/the-moment.db .dev/db/
```

### Launch configurations

Two relevant configs in `.vscode/launch.json`:

| Config | Mode | Use when |
|---|---|---|
| **Run The Moment (debug)** | `exec` — runs the pre-built binary | Testing LAN features; macOS firewall allows signed binaries on the LAN interface |
| **Run The Moment (auto - tests)** | `auto` — compiles and runs via `go run` | Quick iteration; LAN access not needed |

The `exec` mode requires the binary to be built and signed first. The **Build, Sign, and Start Spoolman** pre-launch task handles this.

Both configs load `.env` for Spoolman URL, port, and other settings, then override the three data path vars with the `.dev/` paths shown above.

_Source: `.vscode/launch.json`_

---

## Environment variable reference

All variables are optional. Defaults apply when the variable is not set.

_Source: `config.go` (`getDBFilePath`, `getGcodePath`, `getUploadsPath`), `constants.go`, `docker-compose.yml`, `Makefile`_

### The Moment data paths

| Variable | Read by | Default (native) | Docker container value | Purpose |
|---|---|---|---|---|
| `THE_MOMENT_DB_PATH` | `config.go`, `docker-compose.yml` | `./the-moment-data/db` | `/app/data/db` | Directory for `the-moment.db`. The DB filename is appended automatically. |
| `THE_MOMENT_GCODE_PATH` | `config.go`, `docker-compose.yml` | `./the-moment-data/gcode` | `/app/data/gcode` | Root directory for print history file attachments: gcode, bgcode, slicer project files. Files are stored under `print-files/YYYY/MM/`. |
| `THE_MOMENT_UPLOADS_PATH` | `config.go`, `docker-compose.yml` | `./the-moment-data/uploads` | `/app/data/uploads` | Root directory for virtual printer uploaded files. Reserved; virtual printer files currently stored as SQLite BLOBs. |

In Docker, host paths set in `.env` are bind-mounted into the container at the fixed paths above. The container always sees `/app/data/db`, `/app/data/gcode`, `/app/data/uploads`, and `/app/data/backups` regardless of what the host paths are.

### Ports

| Variable | Read by | Default | Purpose |
|---|---|---|---|
| `THE_MOMENT_PORT` | `docker-compose.yml`, `main.go` | `5000` | Port The Moment listens on (host-side in Docker; direct port in native mode). |
| `SPOOLMAN_PORT` | `docker-compose.yml` | `7912` | Host-side port for Spoolman. Spoolman always listens on `8000` inside its container; this variable remaps it on the host. |

### Spoolman data path

| Variable | Read by | Default | Purpose |
|---|---|---|---|
| `SPOOLMAN_DB_PATH` | `docker-compose.yml` | `./spoolman-data` | Host directory bind-mounted to `/home/spoolman/data` inside the Spoolman container. Contains Spoolman's SQLite database. |

### Runtime behavior

| Variable | Read by | Default | Purpose |
|---|---|---|---|
| `SPOOLMAN_URL` | `main.go` (first-run seed only) | `http://localhost:7912` | URL The Moment uses to reach Spoolman. Only applied when the database is first created; ignored on subsequent starts. In Docker compose this is set to `http://spoolman:8000` (Docker DNS). Change via Settings → Advanced after first run. |
| `SPOOLMAN_EXTERNAL_URL` | `main.go` (first-run seed only) | `http://localhost:7912` | Browser-reachable Spoolman URL. Used for "Open Spoolman" links in the UI. In Docker compose this defaults to `http://spoolman:8000`. Falls back to `SPOOLMAN_URL` when empty. |
| `BAMBU_DEBUG` | `bambu.go` | `0` | Set to `1` for verbose Bambu MQTT debug logging (requires restart). Hot-togglable without restart via Settings → Advanced → `bambu_debug = true`. |

### Backup

| Variable | Read by | Default (native) | Docker container value | Purpose |
|---|---|---|---|---|
| `BACKUP_DIR` | `Makefile`, `config.go`, `docker-compose.yml` | `./backups` | `/app/data/backups` | Directory where backup archives are stored (created by in-app backup, `make backup`, or `make backup-native`). Point at a network share or a path your backup agent monitors. |

---

## Data directory layout

When using defaults, all The Moment data sits under one root on the host (`./the-moment-data`):

```
the-moment-data/
  db/
    the-moment.db          ← SQLite database (all config, history, mappings)
  gcode/
    print-files/
      2026/
        05/
          42_my-print.gcode
  uploads/
    (virtual printer files — future use)
spoolman-data/             ← Spoolman's database (separate service)
```

Each path can be relocated independently via environment variables. For example, put gcode on a NAS:

```env
THE_MOMENT_DB_PATH=./fast-ssd/db
THE_MOMENT_GCODE_PATH=/mnt/nas/the-moment-gcode
THE_MOMENT_UPLOADS_PATH=./fast-ssd/uploads
```

---

_See also: [README](../README.md) · [CONTRIBUTING](../CONTRIBUTING.md) (developer guide)_
