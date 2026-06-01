# Claude Code Skills — Cheat Sheet

Skills are reusable Claude workflows invoked with `/skill-name` in chat.
They auto-trigger when your phrasing matches the skill's description, or you can invoke them explicitly.
Skills live in `~/.claude/skills/<name>/SKILL.md` and are available in every Claude Code session.

---

## How to invoke

| Method | Example |
|---|---|
| Explicit slash command | `/release` |
| Natural language (auto-detect) | "let's wrap up this session" → triggers `/wrap-up` |
| With arguments | `/code-review high` or `/code-review --fix` |

---

## Your Skills (project-specific workflows)

| Command | When to use | What it does |
|---|---|---|
| `/release` | Ready to ship a version | Runs tests, checks version.go, scans for issues, tags, pushes to GitHub, triggers Actions |
| `/interview` | Starting a new feature | Asks core problem / success / out-of-scope before any code is written |
| `/wrap-up` | End of a work session | Reviews diff, updates CLAUDE.md, writes docs, proposes a real commit message |
| `/dev` | Starting local development | Guides through `make dev-build` / `make dev-up`, ports, hot-reload, troubleshooting |
| `/create-doc-page` | "Document how X works" | Reads source code and writes a grounded doc page into `docs/` |

---

## Built-in Skills (always available)

| Command | When to use | Options / Notes |
|---|---|---|
| `/verify` | After building a feature | Runs the app and checks the feature actually works, not just tests |
| `/run` | Need to see the app running | Launches the app; defers to project-level knowledge if available |
| `/code-review` | Before committing | `low` / `medium` / `high` / `max` / `ultra` effort; `--fix` applies changes; `--comment` posts to PR |
| `/simplify` | Diff feels overbuilt | Shorthand for `/code-review --fix` |
| `/security-review` | Pre-release or after auth changes | Reviews current branch diff for security issues |
| `/review` | PR ready | Reviews a pull request; pass PR number for GitHub PRs |
| `/schedule` | Automate a recurring task | Creates a cron-scheduled remote agent |
| `/loop` | Poll something repeatedly | Runs a command on an interval (e.g. `/loop 5m /verify`) |
| `/fewer-permission-prompts` | Too many "allow?" dialogs | Scans transcripts and adds an allowlist to settings.json |
| `/init` | Brand new repo | Generates a CLAUDE.md from scratch |
| `/update-config` | "From now on, whenever X..." | Configures hooks/permissions in settings.json |

---

## How skills work

```
~/.claude/skills/
  release/
    SKILL.md       ← frontmatter: name, description (trigger phrases)
                      body: step-by-step instructions Claude follows
  interview/
    SKILL.md
  wrap-up/
    SKILL.md
  dev/
    SKILL.md
  code-doc-page/
    SKILL.md
    references/    ← optional: project hints, templates, reference data
```

**Auto-trigger:** Claude reads the `description:` field of every skill and will invoke the
skill automatically when your message matches. You don't have to remember the slash command —
just describe what you want.

**Creating a new skill:**
1. `mkdir ~/.claude/skills/my-skill`
2. Write `SKILL.md` with YAML frontmatter (`name:`, `description:`) and step-by-step body
3. Available immediately in the next message — no restart needed

---

## The Moment — dev quick reference

```bash
make dev-build    # build image (first time, or after go.mod changes)
make dev-up       # start dev stack with hot-reload (foreground, Ctrl-C to stop)
make dev-down     # stop dev stack
make test-all     # run unit + integration tests
make up           # start production stack
make down         # stop production stack
make backup       # archive data to ./backups/
```

Dev URLs: The Moment → `http://localhost:5001` · Spoolman → `http://localhost:5002`

Release: `git tag v1.0.0 && git push origin v1.0.0` (triggers Docker build + GitHub Release)
