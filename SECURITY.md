# Security Policy

## Supported Versions

| Version | Supported |
|---|---|
| v1.0.0-alpha | Yes |

## Reporting a Vulnerability

The Moment is a LAN-only service with no authentication layer — it is designed to run on a trusted home or lab network behind a firewall. It should **never** be exposed directly to the public internet.

If you discover a security vulnerability, please **do not open a public GitHub issue**. Instead:

1. Email the maintainer at the address on the [GitHub profile](https://github.com/ThetaSigmaLabs)
2. Include: a description of the vulnerability, steps to reproduce, and your assessment of impact
3. You will receive a response within 7 days

Security fixes are applied to the latest release. Once patched, the vulnerability will be disclosed publicly in the release notes with credit to the reporter (unless you prefer to remain anonymous).

## Scope

In scope:
- Vulnerabilities that could allow data exfiltration or code execution from the LAN
- Authentication bypass (if authentication is added in a future version)
- SQL injection or path traversal
- XSS in the web UI

Out of scope:
- Attacks that require physical access to the host
- The Bambu MQTT TLS `InsecureSkipVerify` flag — this is intentional (Bambu printers use self-signed certificates); see `bambu.go` for the relevant comment
- Vulnerabilities in Spoolman (report those to the [Spoolman project](https://github.com/Donkie/Spoolman))
