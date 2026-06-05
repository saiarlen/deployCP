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
| **Operations** | Per-platform health history, Git deploy hooks, deploy keys, backups/restores, alert events, and repair |
| **Port Info** | Admin-only managed runtime port availability plus per-service CPU, RAM, disk, and working-directory visibility |
| **Host Hardening** | Automatic firewall bootstrap, fail2ban, logrotate, backup cron, SSH-safe install flow |

Runtime behavior on live Linux:

- runtime selectors and Settings runtime lists only show versions actually installed on the host
- detected host default runtimes for Go, Node, Python, and PHP CLI are auto-imported into DeployCP's managed runtime list
- host-imported runtime entries are marked as protected and cannot be removed from Runtime Version Management
- fresh install attempts to install at least one real PHP-FPM version by default
- runtime add/remove actions are real host operations
- PHP runtime add choices come from package-manager PHP-FPM availability; installed PHP-FPM versions are detected from installed packages, not leftover `/etc/php` config directories
- on Ubuntu, Settings provides a PHP repository refresh action that enables `ppa:ondrej/php` and updates apt metadata so additional PHP-FPM versions can be discovered before install
- runtime removal is blocked if a platform is still using that version
- changing the system-wide Python default is intentionally disabled because it can break Linux OS and desktop dependencies
- per-platform runtime selection is applied through `<platform-home>/.deploycp/runtime.env`
- site-user SSH and extra SSH users for the same platform read the same platform runtime env
- Go, Node, Python, and Binary/Other platforms do not create an application service during initial platform creation; the service is created only from Runtime Setup after an app binary/script and port are chosen
- linked app services run as the platform's active primary site user when one exists; root still has access, and additional SSH/FTP users keep shared group access to the platform tree
- platform runtime type is fixed after creation, while the runtime version can be changed later from platform settings
- changing a platform runtime version updates the site-user SSH environment and, when a runtime service exists, rewrites/restarts the service configuration to use the selected version
- deleting a linked runtime stops/disables/removes the service and unit, removes scoped sudoers and app logs, removes DeployCP-managed Python/Node runtime metadata, clears proxy/runtime fields, and refreshes nginx back to static `htdocs` serving
- platform/user/FTP/database create flows roll back external resources when the panel database step fails, so failed creates should not leave orphan Linux users, FTP users, managed DB users, or partial platform rows
- Runtime Setup checks both DeployCP-managed port conflicts and live local port availability before saving
- Settings includes an admin-only Port Info tab for managed platform runtime ports only; it does not enumerate unrelated system ports such as nginx, SSH, database, or mail listeners
- Port Info checks a requested port against DeployCP-managed runtime assignments and live loopback bind availability before reporting it available
- Port Info service usage is read-only: CPU is averaged since service start, RAM comes from the service process tree, and disk usage is the runtime working directory size
- PHP websites use real host `php-fpm`; Settings runtime add/remove owns PHP-FPM package installation/removal for managed PHP versions
- PHP runtime install is package-managed only: it installs/repairs PHP-FPM and PHP CLI packages, verifies FPM is active, and registers a CLI wrapper without compiling PHP from source
- first install registers the bootstrap PHP runtime as a package-managed DeployCP runtime, not as a protected host import
- PHP-FPM platform choices are limited to already installed FPM packages on live Linux; managed PHP CLI catalog entries, leftover FPM config directories, and package-manager-available-but-not-installed versions are not enough to create a PHP website
- PHP platform create/update never installs PHP-FPM implicitly; install the PHP version from Settings first, then select it on the platform
- PHP platform tuning settings are written to `<platform>/htdocs/.user.ini` so PHP-FPM applies them per site
- removing a managed PHP version purges the matching safe versioned FPM/CLI package and clears stale systemd unit state; shared generic packages such as `php-fpm` are skipped
- if a PHP website shell still falls back to a managed PHP CLI version, DeployCP blocks removing that managed version
- direct `systemd` runtime platforms are verified more strictly than `pm2`, `gunicorn`, and `uwsgi`, which remain best-effort verified from live process inspection
- GitHub Actions and SSH deploy flows can call `deploycp deploy` or `deploycp runtime restart` on the target host instead of calling `systemctl` directly
- per-platform Operations stores health history, backup records, deploy configuration, and alert events in SQLite
- per-platform deploy keys are written under `<platform-home>/.deploycp/deploy/`, stored encrypted in panel metadata, and used through `GIT_SSH_COMMAND` during Git deploy
- per-platform backup archives are stored under DeployCP's backup root, include platform files plus attached MariaDB/Postgres and Redis data dumps, and restore uses path traversal and symlink escape checks before writing files
- admin-only repair regenerates nginx config, runtime service state, scoped sudoers, shell runtime files, and filesystem permissions from saved metadata

Python runtime setup:

- Python app services use a per-platform virtualenv at `<platform-home>/.deploycp/python-venv`
- selecting `gunicorn` or `uwsgi` installs that process manager inside the per-platform virtualenv, not globally
- if `htdocs/requirements.txt` exists, Runtime Setup installs those app dependencies into the same virtualenv
- Python `systemd` mode also uses the per-platform virtualenv Python
- changing the Python runtime version recreates only DeployCP's managed virtualenv, then reinstalls the selected process manager and `requirements.txt`
- deleting or resetting the linked runtime removes the managed Python virtualenv without deleting files under `htdocs`

Node runtime setup:

- Node app services use the selected managed Node version from the platform runtime selection
- if `htdocs/package.json` exists, Runtime Setup installs app dependencies with `npm ci --omit=dev` when `package-lock.json` exists, otherwise `npm install --omit=dev`
- selecting `pm2` installs PM2 into `<platform-home>/.deploycp/node-tools` and runs `pm2-runtime`; a global `pm2` install is not required
- `pm2-runtime` units must not receive the normal `pm2 start --exec-mode` flag; worker count is still passed with `-i`
- changing the Node runtime version recreates DeployCP's managed PM2 tools directory and reinstalls PM2
- deleting or resetting the linked runtime removes only the managed Node tools directory; app files and `htdocs/node_modules` are left under the platform owner

## Production Readiness and Security

DeployCP is designed for real Linux servers and controlled multi-app hosting, but it should be operated with the same care as any root-level hosting control panel.

Production-oriented safeguards include:

- per-platform Linux users with restricted shells and scoped SSH access
- per-platform runtime environment files under `.deploycp/runtime.env`
- systemd-managed application services with bounded start/stop/restart controls
- runtime restart and deploy hooks exposed through `deploycp runtime restart` and `deploycp deploy`, so automation does not need raw `systemctl`
- one-click install/update writes a managed `/usr/local/bin/deploycp` wrapper that loads `/home/deploycp/core/.env` and runs the release binary
- nginx config generation with validation before reload
- stale same-domain nginx config cleanup before writing a current vhost
- a managed nginx catch-all that rejects unknown/deleted domains instead of serving the host default page
- domain ownership checks before platform/domain creation
- CSRF protection, authenticated panel routes, session handling, and login rate limiting
- host hardening scripts for firewall, fail2ban, logrotate, and backups
- admin-only per-platform repair, backup/restore, deploy config, deploy execution, and runtime restart actions
- per-platform alert records for service down, SSL expiry/expiration, and high disk usage, with optional JSON webhook delivery

Security notes:

- DeployCP performs privileged host operations and should be installed only on servers you control.
- Use strong admin credentials and keep the panel behind trusted network access when possible.
- Treat private deploy keys as production secrets. Rotate them from the Operations tab when repository access changes.
- `ALERT_WEBHOOK_URL`, when configured, receives alert JSON for new alert events; secure that endpoint the same way you secure other operational webhooks.
- Public multi-tenant hosting with untrusted users should be treated as higher risk until the deployment has been independently audited.
- Review any manually edited nginx, sudoers, runtime, or service files before assuming managed cleanup can repair them automatically.

## Resource Usage

DeployCP itself is lightweight. Most server load comes from hosted applications and managed services, not from the panel process.

Rough estimates for the `deploycp` service:

| Scenario | Approximate usage |
|---|---|
| Idle panel | 20-60 MB RAM, near-zero CPU |
| Normal UI/admin usage | 50-150 MB RAM, short CPU spikes during actions |
| File manager, log viewing, runtime actions, smoke tests | temporary CPU and memory spikes depending on file size and host operations |

Host resource planning depends on the workloads you enable:

- nginx static/proxy hosting is usually low overhead
- Go/binary apps depend mostly on the app itself
- Node, Python, PHP-FPM, pm2, gunicorn, uwsgi, Redis, MariaDB, PostgreSQL, and Varnish add their own memory and CPU requirements
- SQLite metadata storage is small for normal panel use, but activity/log retention and backups need disk planning

Suggested minimums:

| Use case | Suggested server |
|---|---|
| Panel plus light static or small Go apps | 1 vCPU, 1 GB RAM |
| Multiple dynamic apps or small databases | 1-2 vCPU, 2 GB RAM |
| Several production apps with DB/Redis/Varnish | 2+ vCPU, 4+ GB RAM |

These are practical starting points, not hard limits. Size the server for the hosted applications first, then add a small margin for DeployCP and host services.

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
    ├── backups/                  # DeployCP host and per-platform backup archives
    └── tmp/
```

Per-site layout:

```text
/home/deploycp/platforms/sites/<domain>/
├── .deploycp/                    # runtime env, app venvs, DeployCP-managed metadata
│   └── deploy/                    # optional per-platform Git deploy key
├── htdocs/                       # nginx web root
└── logs/                         # per-site access/error logs
```

Important:

- SSH user home points to `/home/deploycp/platforms/sites/<domain>`
- file manager root points to `/home/deploycp/platforms/sites/<domain>`
- nginx serves `/home/deploycp/platforms/sites/<domain>/htdocs`
- runtime-backed linked app services run with `htdocs` as their working directory
- the primary platform domain is treated as fixed after creation
- platform settings only allow editing the subpath inside `htdocs`, not the full absolute root path

Runtime-backed platform workflow:

1. Create the platform and choose its runtime type/version.
2. Upload or create the app files in `htdocs`.
3. Use Runtime Setup to choose the process manager, entry point/binary, and local bind port.
4. DeployCP writes the nginx proxy vhost and creates a named systemd service such as `deploycp-app-example-com.service`.
5. The service runs as the platform's primary site user when available.
6. Site users with SSH access can use scoped sudo for `start`, `stop`, `restart`, `status`, and `is-active` on that one service only.

Operations workflow:

1. Open a platform's Operations tab.
2. Run a health check to record service, HTTP, SSL, and disk status.
3. Configure Git deploy with repository URL, branch, optional private deploy key, work directory, and optional deploy command.
4. Trigger deploy from the panel, or from GitHub Actions using `deploycp deploy --platform <id-or-domain>`.
5. Restart the runtime from the panel, or from automation using `deploycp runtime restart --platform <id-or-domain>`.
6. Create and restore per-platform backups from the Operations tab. Each archive contains platform files plus MariaDB/Postgres and Redis dumps for connections attached to that platform. Restores create a pre-restore backup first, restore files/data, then run platform repair.
7. Use admin-only Repair Platform when nginx, service units, sudoers, runtime env, or permissions drift from saved metadata.

For Python Flask/uWSGI, a minimal `htdocs` layout is:

```text
htdocs/
├── app.py
└── requirements.txt
```

with `requirements.txt` containing at least:

```text
flask
```

Then choose `uwsgi`, entry point `app:app`, and the app's local port in Runtime Setup.

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
| `internal/services/platform_ops_service.go` | Per-platform health, deploy, backup/restore, alerts, repair |
| `internal/services/port_info_service.go` | Admin-only Settings Port Info data: managed runtime ports, port availability, and service usage |
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

# Restart one platform runtime without raw systemctl
deploycp runtime restart --platform example.com

# Pull configured Git repo, run optional deploy command, and restart runtime
deploycp deploy --platform example.com

# Override branch for one deploy
deploycp deploy --platform example.com --branch main

# Run and persist a per-platform health check
deploycp health-check --platform example.com
```

For CI/CD on the target host, `--platform` can be omitted when the working directory is inside a managed platform root, or supplied through `DEPLOYCP_PLATFORM`. `DEPLOYCP_BRANCH` can supply a branch override for `deploycp deploy`.

The Linux installer creates `/usr/local/bin/deploycp` as a managed wrapper around `/home/deploycp/core/bin/deploycp`, so GitHub Actions can run the short command while still loading the production `.env`.

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

# Restart a specific platform runtime without direct systemctl
sudo /home/deploycp/core/bin/deploycp runtime restart --platform example.com

# Run the configured Git deploy for a platform
sudo /home/deploycp/core/bin/deploycp deploy --platform example.com

# Record a platform health check and update alert state
sudo /home/deploycp/core/bin/deploycp health-check --platform example.com

# Re-apply host hardening on an existing server
sudo /home/deploycp/core/scripts/linux/harden-host.sh
```

### Production Smoke Test

DeployCP also ships with a production-oriented smoke test runner:

```bash
sudo DEPLOYCP_TEST_ADMIN_USER=admin \
     DEPLOYCP_TEST_ADMIN_PASS='your-panel-password' \
     /home/deploycp/core/scripts/linux/tests.sh
```

What it does:

- logs into the panel with a real session, CSRF token, and captcha
- checks major admin pages and platform-manager surfaces
- creates temporary platforms and related resources
- verifies runtime-backed platforms, logs, file manager, SSH/FTP users, cron, SSL, Varnish, and selected database/Redis flows when the matching host services are available
- deletes the temporary resources again and runs `reconcile-managed`

Important:

- it is intended to run on a real server as `root`
- it tries to leave the server in a clean managed state after the run
- runtime removal guard checks are skipped by default because they intentionally hit mutation endpoints; enable them with:

```bash
sudo DEPLOYCP_TEST_ADMIN_USER=admin \
     DEPLOYCP_TEST_ADMIN_PASS='your-panel-password' \
     DEPLOYCP_TEST_ALLOW_RUNTIME_MUTATION=1 \
     /home/deploycp/core/scripts/linux/tests.sh
```

Current limitation:

- alternative managers such as `pm2`, `gunicorn`, and `uwsgi` are still only best-effort verifiable by the product itself, so the smoke test cannot prove them as strictly as direct `systemd` runtime execution

**Recovery order:**

1. `systemctl status deploycp` — check if the service is running
2. `deploycp verify-host` — identify missing binaries, dirs, or config
3. Fix any reported issues (install missing packages, set env values)
4. `deploycp reconcile-managed` — re-sync managed resources
5. `deploycp health-check --platform <domain-or-id>` — record platform health and alert state
6. Test the affected platform workflow

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
- the stock nginx default site is disabled when it is the standard `/var/www/html` fallback
- DeployCP writes a managed nginx catch-all vhost that rejects unknown/deleted domains instead of serving the host default page

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

Per-platform backups are separate from the host-level cron backup but use the same DeployCP backup root (`BACKUP_TARGET_DIR`). They are created from the platform Operations tab, tracked in SQLite, and stored under `/home/deploycp/platforms/backups/<platform>/`.

Per-platform backup scope:

- included: platform home files, `htdocs`, logs under the platform home, `.deploycp` runtime metadata, attached MariaDB/Postgres database dumps, and attached Redis DB dumps
- excluded: per-platform Git private deploy key files, panel SQLite metadata, host packages, nginx global files, systemd global state, and any external database/Redis not registered against that platform in DeployCP
- restore: creates a pre-restore backup first, validates archive paths/symlinks, restores files, restores attached database dumps, flushes and rebuilds attached Redis DBs from the archived dump, removes temporary backup metadata, then runs Repair Platform

The host-level backup script is what captures DeployCP's own SQLite metadata and global host state. Use it when you need disaster recovery for the panel itself; use per-platform backups when you need to roll one app/platform back.

Optional alert webhook:

```env
ALERT_WEBHOOK_URL=https://alerts.example.com/deploycp
```

DeployCP sends JSON only when a new alert opens. Existing alert records remain visible in the platform Operations tab even without a webhook.

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

- install/update now ships a bundled English-only Adminer PHP file instead of relying on distro Adminer packages
- `ADMINER_URL` defaults to `http://127.0.0.1:8081`
- the panel proxies those helpers through authenticated DeployCP routes instead of exposing them directly to the browser
- Adminer is used for both MariaDB and PostgreSQL
- clicking a DB card opens the associated database directly through a panel-generated Adminer auto-login bridge
- `pgweb` / `pgAdmin` are not part of the current DeployCP database-helper flow
- DeployCP can start a local Adminer PHP helper when `php` and a local Adminer install are available
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
│   └── linux/                  # install, update, uninstall, build, runtime-manager, production smoke tests
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
