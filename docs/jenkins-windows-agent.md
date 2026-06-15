# Jenkins Windows Agent Setup

This guide covers setting up Docker Desktop on the Windows Jenkins agent so the pipeline can build and push the `linux/amd64` Docker image.

---

## Prerequisites

- Windows 10/11 (22H2 or later) or Windows Server 2019+
- Jenkins agent running via NSSM under your personal Windows account
- WSL 2 enabled (required by Docker Desktop)

---

## 1. Enable WSL 2

Open an elevated PowerShell prompt:

```powershell
wsl --install
```

Reboot when prompted. After reboot, WSL 2 is the default backend.

If WSL is already installed but set to v1:

```powershell
wsl --set-default-version 2
```

---

## 2. Install Docker Desktop

Download Docker Desktop for Windows from [docker.com/products/docker-desktop](https://www.docker.com/products/docker-desktop) and run the installer.

During installation:
- Keep **Use WSL 2 instead of Hyper-V** selected (recommended)
- Keep **Add shortcut to desktop** if desired

Reboot if the installer requests it.

---

## 3. Verify Docker is running

Open a PowerShell prompt and run:

```powershell
docker version
docker info
```

Both commands should succeed with no errors. If `docker info` shows `ERROR: error during connect`, Docker Desktop is not running — start it from the Start menu.

---

## 4. Configure the insecure registry

The pipeline pushes to a local registry at `10.9.8.8:5050` over plain HTTP. Docker Desktop must be told to allow this.

Open Docker Desktop → Settings → Docker Engine. Add the `insecure-registries` key to the JSON config:

```json
{
  "insecure-registries": ["10.9.8.8:5050"]
}
```

Click **Apply & restart**.

Verify the registry is reachable:

```powershell
docker pull --platform linux/amd64 10.9.8.8:5050/the-moment:latest
```

---

## 5. Allow the Jenkins agent account to use Docker

If the Jenkins agent runs as **your personal Windows account** (the NSSM default), your account is already in the `docker-users` group via Docker Desktop's installer. Confirm:

```cmd
net localgroup docker-users
```

Your username should appear in the list. If it does not, add it:

```cmd
net localgroup docker-users "YourWindowsUsername" /add
```

Replace `YourWindowsUsername` with the output of `whoami` (the part after the backslash).

Restart the Jenkins agent after any group change:

```cmd
nssm restart JenkinsAgent
```

---

## 6. Verify linux/amd64 container support

Docker Desktop on an amd64 Windows host runs linux/amd64 containers natively — no emulation needed.

Test it:

```powershell
docker run --rm --platform linux/amd64 hello-world
```

Expected output ends with `Hello from Docker!`.

---

## 7. Confirm Docker is available from the Jenkins agent

Run a pipeline with this probe step on the `windows` agent:

```groovy
stage('Docker probe') {
    agent { label 'windows' }
    steps {
        bat 'docker version'
        bat 'docker run --rm --platform linux/amd64 hello-world'
    }
}
```

If both pass, the agent is ready for the `Build Docker Images` and `Test Docker Images` stages.

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `docker: command not found` in Jenkins | Docker Desktop not installed, or its bin path not in the NSSM service PATH. Add `C:\Program Files\Docker\Docker\resources\bin` to the system PATH and restart the agent. |
| `error during connect` | Docker Desktop not running. Enable **Start Docker Desktop when you log in** in Settings → General, or add a startup task. |
| Push fails with `http: server gave HTTP response to HTTPS client` | The insecure registry is not configured. See step 4. |
| `permission denied` on the Docker socket | The Jenkins account is not in `docker-users`. See step 5. |
