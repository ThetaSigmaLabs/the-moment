# Backup, Restore & Migration

This page covers how to back up The Moment's data, restore from a backup, and migrate to a new PC.

---

## What's in a backup

The Moment owns three data directories:

| Data | Default path | What it contains |
|------|-------------|-----------------|
| Database | `THE_MOMENT_DB_PATH/the-moment.db` | All print history, printer configs, spool assignments, costs, settings |
| G-code files | `THE_MOMENT_GCODE_PATH/` | G-code and slicer file attachments from print history |
| Uploads | `THE_MOMENT_UPLOADS_PATH/` | Files uploaded to virtual (test) printers |

**Spoolman data is not included in The Moment's backup.** Spoolman manages its own database at `SPOOLMAN_DB_PATH`. When you need a complete system backup (e.g. before migrating to a new PC), use `make backup` (Docker) which covers both services.

### Backup scopes

Because the G-code and uploads directories can grow to several gigabytes, every backup lets you choose a scope:

| Scope | Includes | Typical use |
|-------|----------|-------------|
| `db` | Database only | Daily backup, quick checkpoint |
| `all` | Database + G-code + uploads | Full backup before migration |
| `gcode` | G-code files only | Separate backup of large attachment store |
| `uploads` | Virtual printer uploads only | Separate backup of upload store |

---

## Creating a backup

### In-app (all users)

Go to **Settings → Advanced → Backup & Restore**. Select a scope, then click **Create Backup**. The archive appears in the list below and can be downloaded to your PC.

### Docker CLI

```bash
# Full backup: The Moment + Spoolman data + config files (stops services briefly)
make backup

# The Moment database only, no Docker stop required (via in-app API)
make backup-native SCOPE=db

# All The Moment data (db + gcode + uploads), no Spoolman
make backup-native SCOPE=all
```

Backups are saved to `BACKUP_DIR` (default: `./backups/`).

### Native binary

```bash
make backup-native              # database only (default)
make backup-native SCOPE=all    # database + gcode + uploads
make backup-native SCOPE=gcode  # gcode files only
```

### Automated / scheduled backups

**Linux (Docker) — cron:**

```cron
# Daily database-only backup at 2 am
0 2 * * * cd /path/to/the-moment && make backup-native SCOPE=db >> /var/log/the-moment-backup.log 2>&1
```

**Linux (native) — cron using the HTTP API:**

```cron
# Daily database backup via the web API
0 2 * * * curl -s -X POST http://localhost:5000/api/backup/create \
  -H "Content-Type: application/json" \
  -d '{"scope":"db"}' >> /var/log/the-moment-backup.log 2>&1
```

**macOS — launchd:**

Create `~/Library/LaunchAgents/com.the-moment.backup.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.the-moment.backup</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/curl</string>
    <string>-s</string><string>-X</string><string>POST</string>
    <string>http://localhost:5000/api/backup/create</string>
    <string>-H</string><string>Content-Type: application/json</string>
    <string>-d</string><string>{"scope":"db"}</string>
  </array>
  <key>StartCalendarInterval</key>
  <dict><key>Hour</key><integer>2</integer><key>Minute</key><integer>0</integer></dict>
</dict>
</plist>
```

Load it with: `launchctl load ~/Library/LaunchAgents/com.the-moment.backup.plist`

**Windows — Task Scheduler (PowerShell):**

```powershell
$action = New-ScheduledTaskAction -Execute "powershell.exe" -Argument `
  "-Command `"Invoke-WebRequest -Method POST -Uri 'http://localhost:5000/api/backup/create' -ContentType 'application/json' -Body '{\"scope\":\"db\"}'`""

$trigger = New-ScheduledTaskTrigger -Daily -At 2am

Register-ScheduledTask -TaskName "TheMomentBackup" -Action $action -Trigger $trigger -RunLevel Highest
```

---

## Restoring a backup (same PC)

### What "restore" means

Restore performs a **clean replace**: each data directory covered by the backup scope is wiped (`os.RemoveAll`), then the archive is extracted into it. There are no zombie files — the directory after restore contains exactly what the backup contained, nothing more.

Disk space: the restore process needs approximately **2× the uncompressed archive size** available during extraction. The in-app preflight check verifies this automatically before proceeding.

After a restore, the **service must be restarted** to reload the new database and file state. The running process continues on pre-restore data until you restart it.

### In-app

1. Go to **Settings → Advanced → Backup & Restore**.
2. Click **↩ Restore** next to the archive you want.
3. Review the pre-flight summary (scope, uncompressed size, disk space check, what will be replaced).
4. Check the confirmation box, then click **Confirm Restore**.
5. After the restore completes, a banner appears: restart the service.
   - **Docker:** `make down && make up`
   - **Native:** stop the binary (Ctrl+C / kill), then restart it.

To restore from a backup file stored on your PC (not yet on the server), use **Upload a Backup** first, then restore from the list.

### Docker CLI

```bash
make restore BACKUP=./backups/backup-YYYYMMDD-HHMMSS.tar.gz
```

This stops containers, extracts the archive, and restarts.

### Native CLI

**Stop the binary first**, then:

```bash
make restore-native BACKUP=./backups/the-moment-backup-YYYYMMDD-HHMMSS-db.tar.gz
```

Start the binary again once complete.

---

## Migrating to a new PC

Before migrating, decide which backup method you'll use:

- **`make backup` (Docker)** — covers The Moment + Spoolman, recommended for a full migration.
- **Settings → Create Backup (scope=all)** — covers The Moment only; handle Spoolman separately.

### 4a. Docker → Docker (most common)

**Old PC:**

```bash
make backup   # creates ./backups/backup-YYYYMMDD-HHMMSS.tar.gz
```

Copy `backup-*.tar.gz`, `docker-compose.yml`, and `.env` to the new PC.

**New PC:**

```bash
# Install Docker, then:
make setup
make restore BACKUP=./backups/backup-YYYYMMDD-HHMMSS.tar.gz
make up
```

Update `.env` if the new PC uses different paths or ports.

### 4b. Docker → Native

**Old PC:**

```bash
make backup   # includes Spoolman
```

Copy the archive and note your `.env` values.

**New PC:**

1. Build or download the The Moment binary.
2. Set environment variables to match your desired paths:
   ```bash
   export THE_MOMENT_DB_PATH=/home/user/the-moment-data/db
   export THE_MOMENT_GCODE_PATH=/home/user/the-moment-data/gcode
   export THE_MOMENT_UPLOADS_PATH=/home/user/the-moment-data/uploads
   ```
3. Extract the archive into those directories:
   ```bash
   tar -xzf backup-YYYYMMDD-HHMMSS.tar.gz \
     --strip-components=0 -C /  # or adjust paths
   ```
   If the old paths differ from the new ones, extract manually and copy each directory.
4. Install and configure Spoolman separately, pointing it at the restored `spoolman-data` directory.
5. Set `SPOOLMAN_URL` to the new Spoolman address and start the binary.

### 4c. Native → Native

**Old PC** — create a backup using the in-app UI or CLI:

```bash
make backup-native SCOPE=all
# → ./backups/the-moment-backup-YYYYMMDD-HHMMSS-all.tar.gz
```

Transfer the archive to the new PC.

**New PC:**

1. Set the same environment variables as the old PC (or adjust to your new paths).
2. Stop any running instance of The Moment binary.
3. Run:
   ```bash
   make restore-native BACKUP=./backups/the-moment-backup-YYYYMMDD-HHMMSS-all.tar.gz
   ```
4. Start the binary.

### 4d. Native → Docker

**Old PC:**

```bash
make backup-native SCOPE=all   # The Moment data only
# Back up Spoolman separately (see below)
```

**New PC:**

1. Install Docker. Run `make setup` to create data directories.
2. Stop the containers (`make down`).
3. Extract The Moment data into the Docker bind-mount directories:
   ```bash
   tar -xzf the-moment-backup-YYYYMMDD-HHMMSS-all.tar.gz
   # Move db/, gcode/, uploads/ into the paths from your .env
   ```
4. Place the Spoolman backup at `SPOOLMAN_DB_PATH`.
5. Start with `make up`.

---

## Spoolman data

`make backup` (Docker) automatically backs up Spoolman's database directory alongside The Moment's data.

For native installations or if you only used the in-app backup (which does not include Spoolman), back up Spoolman separately:

```bash
# Copy Spoolman's data directory
cp -r "$SPOOLMAN_DB_PATH" /path/to/backup/spoolman-data/

# Or tar it
tar -czf spoolman-backup-$(date +%Y%m%d).tar.gz "$SPOOLMAN_DB_PATH"
```

When restoring Spoolman on the new PC, extract the backup to the directory Spoolman is configured to use before starting the Spoolman container or service.

---

## Data directory reference

| Variable | Default | Docker container path |
|----------|---------|----------------------|
| `THE_MOMENT_DB_PATH` | `./the-moment-data/db` | `/app/data/db` |
| `THE_MOMENT_GCODE_PATH` | `./the-moment-data/gcode` | `/app/data/gcode` |
| `THE_MOMENT_UPLOADS_PATH` | `./the-moment-data/uploads` | `/app/data/uploads` |
| `SPOOLMAN_DB_PATH` | `./spoolman-data` | `/home/spoolman/data` |
| `BACKUP_DIR` | `./backups` | `/app/data/backups` |
