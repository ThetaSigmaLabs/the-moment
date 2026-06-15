# Prusa Forum Post Draft

**Target board:** Prusa Connect - PrusaLink  
**URL:** https://forum.prusa3d.com/forum/prusa-connect-prusalink/

---

## Post title

[Tool] The Moment — automatic filament tracking and cost logging for PrusaLink printers

---

## Post body

Hi all,

I wanted to share a self-hosted tool I've been running alongside a CORE One L (and an Ender 3 with OctoPrint).

**The Moment** sits between your PrusaLink printer and [Spoolman](https://github.com/Donkie/Spoolman). When a print finishes it automatically deducts filament from the right spool in Spoolman, logs the print with duration and a cost breakdown (filament + electricity + maintenance), and stores the G-code thumbnail if your slicer embedded one. Nothing to touch manually.

It also supports NFC tags — tap a spool on your iPhone, tap the printer slot, and the spool is assigned. Spoolman's location field updates at the same time.

Tested on CORE One L (single toolhead) and Prusa XL (5 toolheads). OctoPrint printers are also supported; Bambu MQTT support is included but not yet hardware-tested.

Deploy is three commands if you have Docker:

```
curl -O https://raw.githubusercontent.com/ThetaSigmaLabs/the-moment/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/ThetaSigmaLabs/the-moment/main/.env.example && cp .env.example .env
docker compose up -d
```

Spoolman is bundled in the compose file — you get both services with those three commands.

GitHub: https://github.com/ThetaSigmaLabs/the-moment

It's a fork of [FilaBridge](https://github.com/needo37/filabridge) by needo37, extended significantly. GPL-3.0. Happy to answer questions.

---

## Notes for editing before posting

- Attach a screenshot of the dashboard or the filament status tab
- Link to specific docs pages once the repo is public and indexed
- Consider adding "I've been running this for several months" once you have real runtime data
- Tone check: modest, no hype, let the feature list speak
