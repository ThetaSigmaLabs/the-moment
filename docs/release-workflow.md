# Release Workflow — The Moment

## Overview

Two release tracks:
- **Stable** (v1.0.x) — bug fixes only, production-ready, Docker `:latest`
- **Beta** (v1.1.0-beta.N) — new features under test, Docker `:beta` only

GitHub: stable = regular release. Beta = pre-release (marked with a badge).

Single source of truth for version: `version.go` line 9. Touch nothing else.

---

## Make targets reference

```
make version             Current version.go, recent tags, commits since last stable tag
make changelog-preview   Draft CHANGELOG entry from commit messages since last stable tag
make github-push         Squash + force-push to GitHub main (no tag, no Docker build)
make github-release      Stable release: push + tag + GitHub Release + Docker :latest
make github-release-beta Pre-release: push + tag + GitHub pre-release + Docker :beta
make github-dispatch     Test build: github-push then trigger Actions → Docker :sha-*
make private-add         Add FILE=<path> to private_files exclusion list
```

---

## Step-by-step release procedures

### Where to start every time

```bash
make version
```

This shows: current version.go value, recent tags, and all commits since the last stable tag.
Use it to decide the next version number:

| What the log shows | Bump | Example |
|--------------------|------|---------|
| All `fix:` commits only | PATCH | v1.0.0 → v1.0.1 |
| Any `feat:` commit | MINOR | v1.0.0 → v1.1.0 |
| Breaking API change | MAJOR | rare |

---

### Stable patch release (v1.0.1 example)

**You do NOT need to avoid developing on main.** Continue working on main as you always have.
Feature branches for bigger work, quick fixes directly on main — that's fine.

There are two cases depending on the state of main at release time:

#### Case A — main only has completed work (most common)

Everything on main is finished and ready to ship. Release directly from main:

```bash
# 1. Check what's unreleased
make version

# 2. Draft the CHANGELOG entry
make changelog-preview
# Review output: remove CI-only items, add user-facing context, capitalise

# 3. Edit CHANGELOG.md — add [v1.0.1] section above [v1.0.0]
# Edit version.go → "1.0.1"

git add version.go CHANGELOG.md
git commit -m "chore: bump to v1.0.1"

# 4. Release
make github-release
# → Squashes to GitHub, tags v1.0.1, creates GitHub Release, Docker :v1.0.1 + :latest

# 5. Confirm on Odroid
docker pull ghcr.io/thetasigmalabs/the-moment:v1.0.1
docker compose up -d
```

#### Case B — main has unfinished work mixed with completed fixes (occasional)

Example: main has `fix-A`, `fix-B`, and `wip-feature-X` (half-done). You want to release
fix-A and fix-B as v1.0.1 without shipping the unfinished feature. Use a short-lived branch:

```bash
# 1. Create a temporary branch from the last stable release tag
git checkout -b release/1.0 v1.0.0-src

# 2. Cherry-pick only the specific fix commits you want
git cherry-pick <hash-of-fix-A>
git cherry-pick <hash-of-fix-B>
# (get hashes from: git log main --oneline)

# 3. Draft CHANGELOG and bump version on this branch
make changelog-preview
# Edit CHANGELOG.md and version.go → "1.0.1"

git add version.go CHANGELOG.md
git commit -m "chore: bump to v1.0.1"

# 4. Release FROM THIS BRANCH
make github-release

# 5. Confirm on Odroid
docker pull ghcr.io/thetasigmalabs/the-moment:v1.0.1
docker compose up -d

# 6. Done — delete the temporary branch (fixes are already on main)
git checkout main
git branch -d release/1.0
```

The `release/1.0` branch is purely temporary — it exists only while you prepare and ship the patch.
The fixes stay on main (you cherry-picked FROM main commits, not away from it).
After deleting the branch, main continues with both the fixes and the in-progress feature.

---

### Beta release (v1.1.0-beta.1 example)

Betas share new features for testing before declaring them stable.
Develop on main (or a feature branch merged to main).

```bash
# 1. Check what's unreleased
make version

# 2. Preview CHANGELOG
make changelog-preview

# 3. Edit CHANGELOG.md — add [v1.1.0-beta.1] section
# Edit version.go → "1.1.0-beta.1"

git add version.go CHANGELOG.md
git commit -m "chore: bump to v1.1.0-beta.1"

# 4. Pre-release
make github-release-beta
# → Docker :v1.1.0-beta.1 + :beta (NOT :latest)

# 5. Confirm on Odroid
docker pull ghcr.io/thetasigmalabs/the-moment:v1.1.0-beta.1
docker compose up -d

# If issues found: fix, bump to v1.1.0-beta.2, repeat from step 3
```

---

### Promoting beta to stable (v1.1.0)

```bash
# 1. Make sure feature branch is merged to main
# 2. Edit version.go → "1.1.0"
# 3. Consolidate all beta CHANGELOG notes into one clean [v1.1.0] section
#    (replace the individual [v1.1.0-beta.N] entries or collapse them)

git add version.go CHANGELOG.md
git commit -m "chore: bump to v1.1.0"

make github-release
# → Docker :v1.1.0 + :latest (beta tags remain as historical pre-releases on GitHub)
```

---

### Test Docker build without releasing

Use this when you're unsure if the build process works and don't want to commit to a version yet.

```bash
make github-dispatch
# → Runs github-push (updates GitHub main), then triggers Actions
# → Docker image built and tagged :sha-XXXXXXX only (no :latest, no :beta)
```

Finding the SHA:
- GitHub → Actions tab → click the triggered run → SHA shown at top of run page
- Or: `gh run list --repo ThetaSigmaLabs/the-moment`

On Odroid:
```bash
docker pull ghcr.io/thetasigmalabs/the-moment:sha-XXXXXXX
docker compose up -d
```

When satisfied, run the normal release:
```bash
# Bump version.go, update CHANGELOG, then:
make github-release   # re-runs github-push with latest local changes
```

---

### Adding a private file

Files in `private_files` are stripped from every GitHub push. The file lists itself.

```bash
make private-add FILE=docs/my-notes.md
```

---

## Version audit trail

Check in this order when confused about what's released:

1. `make version` — current state at a glance (version.go vs. tags vs. unreleased commits)
2. `CHANGELOG.md` — human-readable summary of each version with dates
3. `git tag --sort=-creatordate` — every tag ever pushed
   - Tags ending in `-src` mark the local main commit at release time
   - Tags without `-src` mark the squashed GitHub commit

---

## Docker tag reference

| Tag | When it updates |
|-----|-----------------|
| `:latest` | Every stable release (no `-` in version: v1.0.1, v1.1.0) |
| `:beta` | Every pre-release (contains `-beta.`) |
| `:vX.Y.Z` | Always (exact version tag, every release) |
| `:sha-XXXXXXX` | Every build (tag push or workflow_dispatch) |

`docker-compose.yml` on Odroid uses `:latest` — it gets the new stable image automatically
when you run `docker compose pull && docker compose up -d` after a stable release.
