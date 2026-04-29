<p align="center">
  <strong>DeployCP</strong><br>
  Self-hosted Linux hosting and cloud management panel
</p>

<p align="center">
  <a href="https://github.com/saiarlen/deployCP/releases/latest"><img src="https://img.shields.io/github/v/release/saiarlen/deployCP?style=flat-square" alt="Latest Release"></a>
  <a href="https://github.com/saiarlen/deployCP/actions/workflows/release.yml"><img src="https://img.shields.io/github/actions/workflow/status/saiarlen/deployCP/release.yml?style=flat-square&label=build" alt="Build Status"></a>
  <a href="https://github.com/saiarlen/deployCP/blob/main/LICENSE"><img src="https://img.shields.io/github/license/saiarlen/deployCP?style=flat-square" alt="License"></a>
  <img src="https://img.shields.io/badge/platform-linux-blue?style=flat-square" alt="Platform">
  <img src="https://img.shields.io/badge/go-1.25+-00ADD8?style=flat-square&logo=go&logoColor=white" alt="Go Version">
</p>

---

DeployCP is a single-binary control panel that manages real Linux server infrastructure from a web UI. It provisions site users, nginx configs, SSL certificates, runtimes, databases, cron jobs, firewall rules, FTP accounts, Redis instances, Varnish caching, and systemd services — with full cleanup on delete.

Built with Go, Fiber, Jet templates, GORM, and SQLite. No external dependencies beyond standard Linux packages.

## Quick Start

**One-click install** (latest release):

```bash
curl -fsSL https://raw.githubusercontent.com/saiarlen/deployCP/main/scripts/linux/install-remote.sh | sudo bash
```

The remote installer downloads the matching release tarball for the host architecture and verifies the published SHA-256 checksum before extraction.

**Pin a specific version:**

```bash
curl -fsSL https://raw.githubusercontent.com/saiarlen/deployCP/main/scripts/linux/install-remote.sh | sudo DEPLOYCP_VERSION=v1.0.0 bash
```

**Update an existing installation:**

```bash
curl -fsSL https://raw.githubusercontent.com/saiarlen/deployCP/main/scripts/linux/install-remote.sh | sudo bash -s -- --update
```

Dashboard update uses the same release/update path. A successful in-panel update already runs host bootstrap and managed-resource reconciliation as part of the update flow, so you normally do not need to run `bootstrap-host` or `reconcile-managed` manually after it finishes.

**Uninstall:**

```bash
sudo /home/deploycp/core/scripts/linux/uninstall.sh
```

After install, open `http://your-server-ip:2024` to create your admin account and start using the panel.

## What It Does

| Area | What DeployCP manages |
|---|---|
| **Platforms** | Create, update, delete websites and apps from a unified UI |
| **Site Users** | Real Linux users with restricted shells and scoped SSH access |
| **Runtimes** | Side-by-side Node, PHP, Python, Go versions with per-platform defaults |
| **Nginx** | Config generation, validation, reload, SSL termination, cleanup |
| **SSL** | Let's Encrypt via Certbot, imported certificates, self-signed certs |
| **Processes** | systemd units for apps with pm2, gunicorn, uwsgi support |
| **Cron** | Real `/etc/cron.d` entries with generated wrapper scripts |
| **Firewall** | ufw, firewalld, or iptables — auto-detected |
| **Databases** | MariaDB and PostgreSQL provisioning (create DB, user, grants, drop) |
| **FTP** | Real Linux FTP users with managed ProFTPD config |
| **Redis** | Dedicated managed instances with per-platform config |
| **Varnish** | Per-site VCL fragments, aggregate include, validate, reload |
| **Logs** | Real filesystem log paths surfaced in the panel |
| **Host Hardening** | Automatic firewall bootstrap, fail2ban, logrotate, backup cron, SSH-safe install flow |

Runtime behavior on live Linux:

- runtime selectors and Settings runtime lists only show versions actually installed on the host
- fresh install attempts to install at least one real PHP-FPM version by default
- runtime add/remove actions are real host operations
- runtime removal is blocked if a platform is still using that version
- per-platform runtime selection is applied through `<platform-root>/.deploycp/runtime.env`
- site-user SSH and extra SSH users for the same platform read the same platform runtime env
- PHP websites use real host `php-fpm`; PHP CLI/runtime management is separate from PHP-FPM service management
- if a PHP website shell still falls back to a managed PHP CLI version, DeployCP blocks removing that managed version
- direct `systemd` runtime platforms are verified more strictly than `pm2`, `gunicorn`, and `uwsgi`, which remain best-effort verified from live process inspection

## Supported Platforms

Release binaries are built for:

| Architecture | Target |
|---|---|
| `linux/amd64` | Standard x86_64 servers and VMs |
| `linux/arm64` | ARM64 servers (AWS Graviton, Oracle Ampere, etc.) |
| `linux/arm/v7` | 32-bit ARM (Raspberry Pi, etc.) |

Tested on Ubuntu, Debian, Rocky Linux, AlmaLinux, CentOS Stream, Fedora, openSUSE, and Arch Linux. The installer auto-detects the package manager (`apt`, `dnf`, `yum`, `zypper`, `pacman`).

## Install Layout

```
/home/deploycp/
├── core/
│   ├── bin/deploycp              # application binary
│   ├── .env                      # configuration (0600)
│   ├── frontend/                 # templates and static assets
│   │   ├── assets/css/
│   │   └── templates/
│   ├── scripts/linux/            # helper scripts (runtime-manager, etc.)
│   ├── docs/                     # HTML documentation
│   └── storage/
│       ├── db/deploycp.sqlite    # metadata database
│       ├── generated/            # cron scripts, htpasswd, redis configs
│       ├── logs/                 # internal logs
│       ├── runtimes/             # installed runtime versions
│       └── ssl/                  # imported/self-signed certificates
└── platforms/
    ├── sites/                    # managed platform root directories
    ├── logs/                     # platform access/error logs
    ├── backups/
    └── tmp/
```

Per-site layout:

```text
/home/deploycp/platforms/sites/<domain>/
├── htdocs/                       # nginx web root
└── logs/                         # per-site access/error logs
```

Important:

- SSH user home points to `/home/deploycp/platforms/sites/<domain>`
- file manager root points to `/home/deploycp/platforms/sites/<domain>`
- nginx serves `/home/deploycp/platforms/sites/<domain>/htdocs`
- the primary platform domain is treated as fixed after creation
- platform settings only allow editing the subpath inside `htdocs`, not the full absolute root path

## Documentation

| Document | Description |
|---|---|
| [Installation Guide](docs/install.html) | Step-by-step install, prerequisites, post-install checklist |
| [Operations Guide](docs/operations.html) | Platform lifecycle, runtime management, service control |
| [Troubleshooting](docs/troubleshooting.html) | Common issues, recovery procedures, log locations |
| [Full Docs](docs/index.html) | Complete reference |

## Architecture

DeployCP is a layered monolith with a clean adapter pattern for OS operations:

```
HTTP Request
  → Middleware (auth, CSRF, session, rate-limit)
    → Handlers (HTTP orchestration only)
      → Services (business logic, provisioning, cross-module workflows)
        → Repositories (DB persistence only)
        → Platform Adapter (linux | darwin | dryrun)
          → System Runner (timeout, audit, stdout/stderr capture)
            → Real OS commands
```

**Key design rules:**
- Handlers never call OS commands directly
- Services own all provisioning logic
- All system commands go through a structured runner with timeout, audit logging, and exit code handling
- OS behavior is isolated behind the platform adapter interface — swap `linux` for `dryrun` with one env var

### Key Source Locations

| File | Purpose |
|---|---|
| `main.go` | Entrypoint and CLI commands |
| `internal/bootstrap/app.go` | Dependency wiring, routes |
| `internal/config/config.go` | Configuration and env loading |
| `internal/models/models.go` | Database schema (GORM models) |
| `internal/platform/linux/manager.go` | Linux adapter (systemd, useradd, nginx) |
| `internal/platform/dryrun/manager.go` | Dry-run adapter for local development |
| `internal/system/command_runner.go` | Safe command execution abstraction |
| `internal/system/nginx/generator.go` | Nginx config generation |
| `internal/services/` | All business logic and provisioning |
| `frontend/templates/` | Jet HTML templates |

## CLI Commands

The binary supports several operational commands beyond serving the web UI:

```bash
# Start the web panel (default)
deploycp serve

# Prepare host after fresh install
deploycp bootstrap-host

# Sync all managed resources to match DB state
deploycp reconcile-managed

# Check host readiness (binaries, dirs, services, config)
deploycp verify-host

# Remove all managed resources (platforms, users, services, firewall rules)
deploycp teardown-managed
```

## Verification and Recovery

```bash
# Check panel service status
sudo systemctl status deploycp --no-pager

# View panel logs
sudo journalctl -u deploycp -n 200 --no-pager

# Run host verification
sudo /home/deploycp/core/bin/deploycp verify-host

# Re-sync all managed state
sudo /home/deploycp/core/bin/deploycp reconcile-managed

# Re-apply host hardening on an existing server
sudo /home/deploycp/core/scripts/linux/harden-host.sh
```

**Recovery order:**

1. `systemctl status deploycp` — check if the service is running
2. `deploycp verify-host` — identify missing binaries, dirs, or config
3. Fix any reported issues (install missing packages, set env values)
4. `deploycp reconcile-managed` — re-sync managed resources
5. Test the affected platform workflow

Use `bootstrap-host` and `reconcile-managed` manually only when:

- an update was interrupted
- you are recovering from older broken state
- you want to force-repair SSH, nginx, runtime, or filesystem state on a live host

## Host Hardening

Fresh installs and updates also converge a few host-level safeguards:

- `fail2ban` is installed and enabled for `sshd`
- `logrotate` keeps DeployCP and platform logs from growing unbounded
- a daily backup job is written to `/etc/cron.d/deploycp-backup`
- backup archives are stored in `/home/deploycp/platforms/backups`

Backup behavior is controlled from `/home/deploycp/core/.env`:

```env
BACKUP_TARGET_DIR=/home/deploycp/platforms/backups
BACKUP_RETENTION_DAYS=14
BACKUP_INCLUDE_SITE_CONTENT=true
BACKUP_INCLUDE_PLATFORM_LOGS=false
BACKUP_PRE_HOOK=
BACKUP_POST_HOOK=
```

Manual backup:

```bash
sudo /home/deploycp/core/scripts/linux/backup.sh
```

## Platform Update Rules

DeployCP intentionally does not treat a saved platform edit as a full domain rename/migration workflow.

- the primary domain is locked after platform creation
- adding or managing extra domains is separate from changing the platform identity
- the platform settings screen only lets you change the path inside `htdocs`
- SSH/file manager root remains the platform root
- nginx web root remains under `htdocs`

This avoids unsafe partial renames where DB rows and nginx move but filesystem paths, users, SSL assets, or cache identity do not.

If the main domain was created incorrectly, the recommended operational flow is:

1. create a new platform with the correct domain
2. move the site content/data
3. verify DNS, SSL, runtime, and users
4. delete the old platform

## Varnish Behavior

DeployCP uses one shared host Varnish service with per-platform rules.

- per-platform VCL fragments are written under `/etc/varnish/deploycp.d/website-<id>.vcl`
- enabling cache writes or updates that platform fragment and reloads Varnish
- disabling cache removes that platform fragment and reloads Varnish
- deleting a platform also removes its Varnish fragment
- disable/delete now also sends a Varnish `ban` for that platform host pattern so cached objects are purged instead of only waiting for TTL expiry

Important:

- cache storage itself is daemon-level, not per-platform filesystem storage
- most Linux installs use shared Varnish memory storage such as `malloc,256m`
- platform-level caching is achieved through per-platform host matching and cache rules, not separate Varnish instances per platform

## Database UI Helpers

DeployCP does not expose DB helpers publicly.

- install/update now attempts to provision local loopback-only DB helpers when distro packages are available
- `ADMINER_URL` defaults to `http://127.0.0.1:8081`
- `POSTGRES_GUI_URL` defaults to `http://127.0.0.1:8082`
- the panel proxies those helpers through authenticated DeployCP routes instead of exposing them directly to the browser
- `pgweb` is used for PostgreSQL when present
- Adminer is used for MariaDB, and DeployCP can start a local Adminer PHP helper when `php` and a local Adminer install are available
- Docker is not used for these tools

## Local Development

Run in dry-run mode to develop on macOS or Linux without real privileged operations:

```bash
# First-time setup
cp .env.example .env
mkdir -p storage/db storage/logs storage/sites

# Run in dry-run mode
PLATFORM_MODE=dryrun go run main.go
```

Dry-run mode redirects all system paths to `./storage/dryrun/` and replaces OS commands with `/bin/echo`. The full UI and business logic runs normally — only the OS-level mutations are simulated.

```bash
# Run tests
go test ./...

# Run vet
go vet ./...
```

## Technology Stack

| Component | Technology |
|---|---|
| Language | Go 1.25+ |
| Web Framework | [Fiber](https://gofiber.io) |
| ORM | [GORM](https://gorm.io) |
| Database | SQLite |
| Templates | [Jet](https://github.com/CloudyKit/jet) |
| Frontend | Tailwind CSS (CDN), Lucide Icons, Notyf, Chart.js |

## Release Process

Releases are automated through GitHub Actions:

1. Tag a commit: `git tag v1.0.0 && git push origin v1.0.0`
2. The [release workflow](.github/workflows/release.yml) builds binaries for all three architectures with CGO cross-compilation
3. Each build produces a tarball containing the binary, frontend assets, scripts, and docs
4. Tarballs and SHA-256 checksums are published to [GitHub Releases](https://github.com/saiarlen/deployCP/releases)
5. The one-click installer downloads the correct tarball for the host architecture

To build locally:

```bash
./scripts/linux/build-release.sh
# Output: dist/deploycp-<version>-linux-{amd64,arm64,armv7}.tar.gz
```

## Repository Layout

```
.
├── main.go                     # entrypoint
├── go.mod / go.sum
├── .env.example                # reference configuration
├── frontend/
│   ├── assets/                 # CSS, JS
│   └── templates/              # Jet HTML templates
├── internal/
│   ├── bootstrap/              # app wiring, DB migrations, seeding
│   ├── config/                 # env loading and validation
│   ├── handlers/               # HTTP handlers
│   ├── middleware/              # auth, CSRF, sessions, rate-limit
│   ├── models/                 # GORM schema
│   ├── platform/               # OS adapters (linux, darwin, dryrun)
│   ├── repositories/           # DB access layer
│   ├── services/               # business logic and provisioning
│   ├── system/                 # command runner, nginx generator
│   ├── utils/                  # crypto, file, path helpers
│   ├── validators/             # input validation
│   └── views/                  # template engine setup
├── scripts/
│   └── linux/                  # install, update, uninstall, build, runtime-manager
├── docs/                       # HTML documentation
├── database/
│   └── migrations/
├── storage/                    # local dev storage (gitignored)
└── .github/
    └── workflows/
        └── release.yml         # CI/CD release pipeline
```

## Contributing

1. Fork the repository
2. Create a feature branch: `git checkout -b feature/my-change`
3. Run in dry-run mode to test: `PLATFORM_MODE=dryrun go run main.go`
4. Run `go test ./...` and `go vet ./...`
5. Submit a pull request

## License

See [LICENSE](LICENSE) for details.
