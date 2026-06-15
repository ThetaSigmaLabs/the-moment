# Git Bare Repo — HEAD / master / main

## The problem

Bare repos initialized with older git default `HEAD` to `refs/heads/master`.
If you push to `main`, HEAD points at a branch that doesn't exist.

Symptom: `fatal: not a valid object name: HEAD`

## The fix (one-time, already applied to NAS repo)

```bash
ssh -p 1192 stephen@10.9.8.8 \
  "git -C /volume1/git/the-moment.git symbolic-ref HEAD refs/heads/main"
```

Verify:

```bash
ssh -p 1192 stephen@10.9.8.8 "cat /volume1/git/the-moment.git/HEAD"
# ref: refs/heads/main
```

## If you ever create another bare repo on the NAS

Run immediately after `git init --bare`:

```bash
git -C /path/to/repo.git symbolic-ref HEAD refs/heads/main
```

## Why Jenkins wasn't affected

Jenkins checks out by branch name (`main`) explicitly — it never uses HEAD.
`git archive HEAD` and `git clone` without a branch flag both rely on HEAD resolving correctly.
