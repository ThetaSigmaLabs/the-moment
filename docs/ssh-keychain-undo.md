# ⚠️ PRIVATE — SSH Key Loaded Into macOS Keychain (UNDO NOTES)

**Date:** 2026-06-09
**What was done:** `~/.ssh/id_rsa` (passphrase-protected) was loaded into `ssh-agent`
with the passphrase stored in macOS Keychain, so git/ssh to the Synology git server
(`local` remote, `ssh://stephen@10.9.8.8:1192`) works without typing the passphrase —
including from non-interactive shells (Claude Code).

```bash
ssh-add --apple-use-keychain ~/.ssh/id_rsa
```

---

## How to UNDO

```bash
# Full undo: remove key from agent AND delete passphrase from macOS Keychain
ssh-add --apple-use-keychain -d ~/.ssh/id_rsa

# Partial: remove from agent only (Keychain keeps passphrase; key reloads on next use)
ssh-add -D

# Check what's loaded
ssh-add -l        # "The agent has no identities." = nothing loaded
```

---

## Temporary alternative (no Keychain persistence)

For one-off tasks, load with an expiry instead — auto-removes, nothing persisted:

```bash
ssh-add -t 2h ~/.ssh/id_rsa
```

---

## Risk scope (why you might want to undo)

- While loaded, the key authenticates to **any** server that trusts `id_rsa.pub`
  (Synology for sure; possibly others — key dates from Dec 2020), not just the git server.
- Any process running as user `stephen` on this Mac can use the agent to sign.
  The private key itself is never exposed; the file on disk stays encrypted.
- With Keychain storage, the key is effectively always available while logged in.
  FileVault still protects everything on a locked/stolen machine.

## Related finding (separate, not yet changed)

`~/.ssh/config` `Host synology` has `ForwardAgent yes` — lets the Synology relay
auth requests back to this Mac's agent during SSH sessions into it. Only needed if
you SSH into the Synology and authenticate onward (e.g. git pull from GitHub on the
NAS using this Mac's key). Remove the line if you never do that.
