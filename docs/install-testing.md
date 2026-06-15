# Install Testing — Local Servers vs GitHub

## Test 1: Local only (no GitHub)

Everything comes from the NAS (10.9.8.8) — files from the local git server, image from the local Docker registry.

### One-time: allow the insecure local registry

Add to `/etc/docker/daemon.json` on the Odroid:

```json
{ "insecure-registries": ["10.9.8.8:5050"] }
```

```bash
sudo systemctl restart docker
```

### Install

```bash
mkdir -p ~/the-moment && cd ~/the-moment

# Pull docker-compose.yml and .env.example from local git server (no clone needed)
ssh -p 1192 stephen@10.9.8.8 \
  "git -C /volume1/git/the-moment.git archive HEAD docker-compose.yml .env.example" \
  | tar -x

# Patch the image to use local registry
sed -i 's|ghcr.io/thetasigmalabs/the-moment:latest|10.9.8.8:5050/the-moment:latest|' docker-compose.yml

cp .env.example .env   # adjust TZ / ports if needed

docker compose up -d
```

---

## Test 2: GitHub (not deployed yet — future commands)

Everything comes from GitHub — files from the public repo, image from GHCR.

### Install

```bash
mkdir -p ~/the-moment && cd ~/the-moment

curl -sLO https://raw.githubusercontent.com/ThetaSigmaLabs/the-moment/main/docker-compose.yml
curl -sLO https://raw.githubusercontent.com/ThetaSigmaLabs/the-moment/main/.env.example
cp .env.example .env   # adjust TZ / ports if needed

docker compose up -d
```

### One-time: make GHCR package public after first tag push

Otherwise `docker pull` will 401:

1. Go to `https://github.com/ThetaSigmaLabs/the-moment/pkgs/container/the-moment`
2. Package settings → Change visibility → Public
