# DeployCP Complete Application Audit

Code-audited feature inventory for the DeployCP panel.

Generated: 2026-06-05  
Scope: Go/Fiber routes, handlers, services, models, templates, frontend assets, CLI entrypoints, and Linux scripts in this repository.

## Audit Scope

This document is written from the application code, not from assumptions about the product. The audit covered:

- HTTP route registration in `internal/bootstrap/app.go`.
- Main application services under `internal/services`.
- Handler workflows under `internal/handlers`.
- Persistent models in `internal/models/models.go`.
- Runtime, deploy, platform, settings, and operation templates under `frontend/templates`.
- Public documentation assets under `docs`.
- CLI commands in `main.go`.
- Linux install, update, hardening, backup, verification, reconcile, and runtime scripts under `scripts/linux`.
- Current go-live sensitive areas: PM2 runtime handling, platform delete cleanup, health check UI, port information, settings UI, deployment commands, and runtime process visibility.

## Executive Feature Map

| Area | Feature coverage | Primary code areas |
| --- | --- | --- |
| Authentication | Setup, login, captcha, sessions, logout, profile, password, theme | `auth_handler.go`, `auth_service.go`, `app.go` |
| Authorization | Admin, site manager, user roles, platform access gates | `middleware`, `panel_user_service.go`, `app.go` |
| Dashboard | Live system cards, charts, historical snapshots, public IP and host facts | `dashboard_service.go`, `dashboard_handler.go` |
| Platforms | Website and app creation, update, delete, toggle, detail pages | `website_service.go`, `app_service.go`, `website_handler.go`, `app_handler.go` |
| Runtimes | PHP, Node, Python, binary apps, runtime versions, process managers | `runtime_service.go`, `app_service.go`, `settings_service.go` |
| Process managers | systemd, PM2, gunicorn, uWSGI, restart, status, logs | `app_service.go`, `service_service.go` |
| Git deploy | Repository config, branch, SSH deploy key, deploy commands, deploy now | `platform_ops_service.go`, platform operation handlers |
| Health checks | Manual checks, runtime health status, history, alerts | `platform_ops_service.go`, operations templates |
| Backups | Platform archive, DB dump, Redis dump, restore workflow | `platform_ops_service.go`, `scripts/linux/backup.sh` |
| Databases | MariaDB, PostgreSQL, credentials, password reset, Adminer bridge | `database_service.go` |
| Redis | Managed Redis connections, credentials, info, password reset, delete | `database_service.go`, `reconcile_service.go` |
| SSL | Let's Encrypt, import, self-signed, renew, delete | `ssl_service.go` |
| Domains and proxy | Primary domain, aliases, linked app proxy, custom vhost | `website_service.go`, nginx config templates |
| Security | IP block, bot block, Basic Auth, Cloudflare mode, panel security | `website_service.go`, `settings_handler.go` |
| FTP | ProFTPD config, FTP users, password reset, delete, reconcile | `ftp_service.go` |
| File manager | Scoped elFinder connector for platform files | `elfinder_handler.go` |
| Cron | Per-platform cron wrapper scripts and `/etc/cron.d` entries | `cron_service.go` |
| Varnish | Per-site cache rules, VCL fragments, reload, purge/ban | `varnish_service.go` |
| Firewall | Panel firewall rules across ufw/firewalld/iptables backends | `firewall_service.go` |
| Settings | General, security, services, users, runtimes, PHP repo, logs, port info | `settings_handler.go`, `settings_service.go` |
| Updates | Release check, install job, status and log tail | `update_service.go` |
| Host lifecycle | Bootstrap, verify, reconcile, teardown managed resources | `host_lifecycle_service.go`, `preflight_service.go`, `reconcile_service.go` |
| Auditability | Audit logs, activity logs, alert events, system metric snapshots | models and services |

## Public And Authenticated Entry Points

### Public routes

| Route | Purpose |
| --- | --- |
| `GET /robots.txt` | Serves panel-level robots behavior from settings. |
| `GET /setup` | First-run setup page when no admin exists. |
| `POST /setup` | Creates the initial administrator account. |
| `GET /login` | Login page. |
| `GET /login/captcha` | Captcha challenge for login protection. |
| `POST /login` | Authenticates with login rate limiting. |
| `POST /logout` | Ends the current session. |
| `POST /theme` | Switches theme without entering the full profile flow. |

### Authenticated routes

| Route group | Purpose |
| --- | --- |
| `/` | Main dashboard. |
| `/dashboard/live` | JSON live system metric feed. |
| `/dashboard/history` | JSON historical system metric feed. |
| `/profile` | User profile display and update. |
| `/profile/password` | Password change screen and action. |
| `/profile/theme` | Per-user theme preference update. |
| `/websites/*` | Classic website/platform management routes. |
| `/platforms/*` | Unified platform pages for sites and apps. |
| `/apps/*` | Application runtime management routes. |
| `/services/*` | Admin-only managed service controls. |
| `/settings/*` | Admin-only panel settings. |
| `/updates/*` | Admin-only update controls. |
| `/logs` | Admin-only redirect to settings event logs. |

## Authentication And Access Control

### First-run setup

- Detects whether the panel needs an initial administrator.
- Creates the bootstrap admin from the setup form.
- Stores users in the `User` model.
- Prevents normal unauthenticated entry into secured routes.

### Login protection

- Uses a dedicated login route with rate limiting.
- Provides a captcha route for bot resistance.
- Starts server-side sessions with IP address and user agent metadata.
- Supports session termination through logout.

### User profile

- Lets signed-in users update name and email.
- Provides password change with current-password verification.
- Persists per-user theme preference through `UserPreference`.

### Roles

- `admin`: full panel and host-operation access.
- `site_manager`: platform-scoped management depending on assigned access.
- `user`: restricted platform-scoped access.

### Platform access

- `UserPlatformAccess` assigns a user to a platform.
- Secured route middleware applies platform access checks.
- Admin-only middleware protects settings, updates, service management, deploy operations, backup/restore, and repair operations.

## Dashboard And Metrics

### Dashboard overview

- Shows high-level counts for platforms, apps, services, and host status.
- Displays OS, hostname, uptime, and public IP details.
- Uses live metric endpoints for current host state.

### Live metrics

- CPU usage.
- RAM usage.
- Disk usage.
- Load average.
- Network throughput in bytes per second.
- Host uptime.

### Historical metrics

- Persists snapshots in `SystemMetricSnapshot`.
- Provides historical chart data through `/dashboard/history`.
- Supports downsampled history for panel charts.

### Collector behavior

- `DashboardService.StartCollector()` runs the metric collector.
- The collector records snapshots for later display.
- The live endpoint can be polled without reloading the dashboard.

## Platform Model

DeployCP uses a unified platform concept while still supporting distinct website and app workflows.

### Website platform

Stored in the `Website` model and table name `platforms`.

Website features include:

- Domain.
- Root path.
- PHP version.
- PHP-FPM pool and socket.
- Proxy target.
- Maintenance mode.
- SSL state.
- Linked app state.
- Site user relationship.
- Runtime shell settings.
- Security settings.
- Varnish settings.
- Cron, database, Redis, FTP, and log relationships.

### Application platform

Stored in the `GoApp` model and also mapped to table name `platforms`.

Application features include:

- Name and platform reference.
- Runtime type: Go, Node, Python, binary, and related app modes.
- Process manager.
- Command or entrypoint.
- Working directory.
- Port and host binding.
- Runtime version.
- Service name.
- Health check path.
- Environment variables.
- Website link when the app is attached behind a site proxy.

## Platform Creation And Management

### Unified platform pages

- `/platforms` lists platform entries.
- `/platforms/new` shows the platform creation flow.
- `/platforms/:ref` resolves either a website or app by reference and displays the correct management page.

### Website creation

Website creation covers:

- Domain validation.
- Root path creation.
- Site user creation.
- PHP-FPM integration.
- Nginx server block generation.
- Optional database provisioning.
- Optional Redis provisioning.
- Audit record creation.

### App creation

App creation covers:

- Runtime category.
- Runtime command.
- Process manager selection.
- Port allocation.
- Working directory setup.
- Runtime scaffold generation.
- Service unit installation.
- Optional site user creation.
- Optional reverse proxy creation when linked to a website.
- Rollback cleanup when creation fails.

### Platform update

Update workflows support:

- Name/domain updates.
- Runtime settings.
- PHP version updates.
- App process settings.
- Port changes with availability validation.
- Proxy settings.
- Maintenance settings.
- Site-user and access updates.

### Platform deletion

Deletion paths are designed to remove platform-specific resources:

- Database rows.
- Generated service units for app runtimes.
- Nginx site configs.
- Runtime scaffolding owned by the platform.
- Site users when owned by the platform workflow.
- Redis connections.
- FTP users.
- SSL records and managed certificate files when deleted through SSL flows.
- Cron jobs.
- Varnish fragments.
- Audit records for the deletion action.

Go-live note: deletion safety depends on correct platform root/path resolution. Runtime code must keep website roots inside `htdocs` and avoid collapsing platform home into the public root.

## Runtime And Process Management

### Runtime categories

DeployCP supports these runtime categories:

- PHP-FPM websites.
- Node applications.
- Python applications.
- Go or binary applications.
- Reverse-proxied applications.

### Runtime versions

Runtime version features include:

- Installed version catalog.
- Available version discovery.
- Runtime version add.
- Runtime version remove.
- Runtime default version selection.
- PHP package repository refresh.
- Import of host/system runtime defaults.
- Runtime wrapper detection.
- Protection for imported host runtimes where applicable.

### Runtime environment

Runtime environment features include:

- PATH merging for managed runtime binaries.
- Runtime wrapper paths.
- App-specific environment variables.
- Platform shell runtime settings.
- Runtime inspection of app services.
- Binary resolution for runtime commands.

### Process managers

| Manager | Covered behavior |
| --- | --- |
| systemd | Outer service supervisor, restart/start/stop/status, memory limits, service logs. |
| PM2 | Node process manager integration under systemd, PM2 runtime state, memory restart support. |
| gunicorn | Python WSGI service command generation and worker controls. |
| uWSGI | Python uWSGI service command generation and worker controls. |
| direct/binary | Direct command execution through generated systemd units. |

### Runtime controls

- Start.
- Stop.
- Restart.
- Status inspection.
- Logs.
- Runtime settings update.
- Reconcile service unit.
- Apply and restart after runtime changes.

## Node And PM2 Runtime Details

### Node runtime setup

- Validates app command and runtime category.
- Prepares managed Node tools.
- Recreates Node tool state when runtime version changes.
- Resolves Node runtime command from the selected runtime environment.
- Chowns Node dependency artifacts to the platform site user where required.

### PM2 behavior

- PM2 is used as an inner process manager for Node apps.
- systemd remains the outer host supervisor.
- PM2 process state is stored under platform-specific runtime state paths.
- PM2 memory restart uses PM2-specific memory restart arguments.
- Port information and health checks should still identify the systemd service and runtime port.

### Next.js standalone deployment considerations

The panel supports command-based app startup. For a Next.js standalone app:

- `output: "standalone"` is required in `next.config.ts` to create `.next/standalone/server.js`.
- `.next/static` must be copied into `.next/standalone/.next/static`.
- `public` contents must be copied into `.next/standalone/public` using `public/.` as the source, not `public` as a nested directory.
- The runtime entrypoint can point to `.next/standalone/server.js`.
- The working directory must remain the deployed app directory under the platform `htdocs` path.

## Python Runtime Details

### Python preparation

- Creates or refreshes virtual environments.
- Recreates the virtual environment when selected runtime version changes.
- Installs dependencies when the runtime workflow requires it.
- Chowns virtual environment files to the correct platform user.

### gunicorn support

- Supports worker count.
- Supports worker class.
- Supports timeout.
- Uses systemd for service supervision.
- Exposes logs through the app log flow.

### uWSGI support

- Supports uWSGI process manager selection.
- Uses systemd for service supervision.
- Supports app command/entrypoint style startup.
- Exposes logs through the app log flow.

## PHP Website Runtime

### PHP-FPM support

- Creates PHP-FPM pool configuration.
- Supports per-platform PHP version.
- Resolves PHP-FPM service names.
- Validates PHP-FPM availability.
- Can apply website PHP runtime changes.
- Supports PHP CLI/runtime wrappers for platform shell usage.

### PHP settings

Per-platform PHP settings include:

- Memory limit.
- Upload limits.
- Post size limits.
- Execution time.
- Custom PHP directives through `.user.ini`.

### PHP package handling

- Detects package manager.
- Finds installable PHP versions.
- Installs PHP CLI/FPM packages for managed runtimes.
- Removes PHP packages when the runtime version is removed.
- Refreshes PHP repositories where supported.

## Git Deploy Operations

### Deploy config

Deploy config is represented by `PlatformDeployConfig` and includes:

- Repository URL.
- Branch.
- Deploy command.
- Working directory or platform-specific deployment path.
- SSH deploy key state.
- Last deploy status.
- Last deploy output.
- Last deploy timestamp.

### SSH deploy keys

- Deploy keys are generated by the panel.
- Private key material is encrypted at rest.
- Public key can be copied into the Git provider.
- Deploy operations use the stored key for repository access.

### Deploy now

Deploy now performs:

- Repository URL configuration.
- Git fetch.
- Branch checkout.
- Hard reset to the selected remote branch.
- User-provided deploy commands.
- Runtime-specific post-deploy operations when configured.
- Last deploy output capture.

Go-live note: deploy commands pasted from Windows or rich text can include CRLF characters. Those characters can break `npm`, `npx`, shell `if`, and `fi` parsing. Use LF-only shell text.

## Health Checks And Operations

### Health check execution

- Manual health check route: `POST /platforms/:ref/manage/ops/health`.
- Health checks resolve the platform reference.
- App health checks use runtime service and health path information.
- Website health checks use platform domain/root context.
- Health checks persist status in `PlatformHealthCheck`.

### Health check data

Each health check can record:

- Status.
- HTTP code.
- Response time.
- Message.
- Checked-at timestamp.
- Platform association.

### Health history UI

- Latest health check is shown as a compact summary.
- Previous checks are available in an accordion/history area.
- History is intentionally not expanded by default to avoid large tables dominating the operations page.
- Backend history is bounded to the latest records for the page.

### Alerts

- Failed checks can create alert events.
- Alert webhook settings are available through configuration.
- Alert state is modeled through `AlertEvent`.

## Backups And Restore

### Platform backup

The operations backup workflow can include:

- Platform files.
- Database dumps.
- Redis dumps.
- Runtime/deploy metadata.
- Backup status and output.

### Platform restore

Restore workflow supports:

- Selecting an available platform backup.
- Restoring files from the backup archive.
- Restoring database and Redis dumps when present.
- Recording restore output.

### Host backup script

The Linux host backup script can cover:

- DeployCP environment files.
- Panel database.
- Generated Nginx files.
- Generated systemd units.
- Generated cron files.
- SSL assets.
- Optional site content.
- Optional logs.
- Retention cleanup.
- Hook execution.

## Repair And Reconcile

### Platform repair

Platform repair can re-apply managed platform resources:

- Nginx configuration.
- Runtime service units.
- Site user access.
- Runtime paths.
- Proxy linkage.
- Varnish configuration.
- Cron entries.

### Host reconcile

The reconcile service covers:

- FTP configuration.
- FTP users.
- Managed Redis.
- Firewall rules.
- Websites.
- Nginx configs.
- Varnish aggregate config.
- Cron jobs.
- App runtime services.

### Host verification

Preflight verification checks:

- Platform mode.
- Root privileges where required.
- Required binaries.
- Firewall backend.
- Package manager.
- Required directories.
- Required files.
- SELinux/AppArmor environment.
- fail2ban state.
- Varnish include hooks.
- Database row counts.
- Database admin configuration.

## Port Info

### Purpose

The Port Info tab is an admin-only operational view for platform runtime ports. It is not a general host port scanner.

### Included rows

Port Info focuses on managed runtime app ports:

- Runtime app name.
- Platform reference.
- Port.
- Runtime category.
- Process manager.
- Service name.
- Service status.
- Main PID.
- CPU usage for the service process tree.
- RAM usage for the service process tree.
- Bounded disk usage for the service path.

### Excluded rows

The view intentionally excludes system ports such as:

- SSH.
- Nginx.
- MariaDB.
- PostgreSQL.
- Redis system service ports.
- Panel service ports.

### Port availability search

Admins can enter a port to check:

- Whether the port is already assigned to a managed runtime app.
- Whether the port appears available for a new platform.
- Whether the query is invalid.

### UI behavior

- Search field supports availability checks.
- Table is paginated.
- Workdir is intentionally not shown in the table.
- Running and stopped counts are summarized.

## Domains, Nginx, Proxy, And Maintenance

### Domain management

- Add platform domains.
- Store domains in `WebsiteDomain`.
- Generate Nginx server names.
- Use domains for SSL issuance and routing.

### Nginx management

- Generates site configuration.
- Supports custom vhost directives for admins.
- Supports proxy mode.
- Supports linked app proxy mode.
- Reloads or validates Nginx as part of managed operations.

### Proxy support

- Websites can proxy to app runtimes.
- Apps can be linked behind websites.
- Proxy target and platform path are tracked in platform settings.

### Maintenance mode

- Supports enabling maintenance mode.
- Supports bypass settings.
- Keeps maintenance behavior platform-scoped.

## SSL Management

### Let's Encrypt

- Creates certificate records.
- Performs HTTP challenge preflight.
- Runs certificate issuance.
- Stores certificate and key paths.
- Marks certificates as issued with expiry metadata.

### Import

- Accepts certificate, private key, and optional bundle.
- Writes managed certificate files.
- Creates or updates certificate records.

### Self-signed

- Generates self-signed certificates.
- Stores generated cert and key paths.
- Records issuer and expiry metadata.

### Renew and delete

- Renew action refreshes managed certificates.
- Delete action removes certificate records and managed files.
- Hooks can run after certificate changes.

## Database Management

### MariaDB

- Creates platform databases.
- Creates users and passwords.
- Stores encrypted credentials.
- Supports password reset.
- Supports deletion.
- Supports backup dump inclusion.

### PostgreSQL

- Creates platform databases.
- Creates users and passwords.
- Stores encrypted credentials.
- Supports password reset.
- Supports deletion.
- Supports backup dump inclusion.

### Adminer bridge

- Provides Adminer URLs for database access.
- Uses tokenized access helpers.
- Supports both MySQL/MariaDB and PostgreSQL workflows.

## Redis Management

### Managed Redis connections

- Creates Redis connection records.
- Assigns port and database index where managed.
- Stores encrypted credentials.
- Supports password reset.
- Supports deletion.
- Supports reconcile.

### Redis info

- Provides a Redis info route for platform Redis records.
- Can display connection/runtime metadata without exposing secrets.

## Site Users And Shell Access

### Site user creation

- Creates Linux users for platform isolation.
- Assigns platform-specific group access.
- Creates expected home/root paths.
- Links users to website/app records.

### Password reset

- Resets site user passwords.
- Audits password reset actions.
- Supports site-level and app-level user flows.

### Shared access

- App services can sync shared access between linked website/app resources.
- Runtime sudoers or helper permissions can be synchronized for platform runtime operations.

## FTP Management

### FTP user workflows

- Create FTP users.
- Reset FTP passwords.
- Delete FTP users.
- List FTP users for a platform.

### ProFTPD integration

- Generates or updates ProFTPD managed configuration.
- Supports masquerade address setting.
- Reconciles FTP users to host state.
- Stores encrypted FTP credentials.

## File Manager

### elFinder connector

The file manager connector supports:

- Open.
- Tree.
- Parents.
- List.
- Make directory.
- Make file.
- Rename.
- Remove.
- Upload.
- Get file.
- Put file.
- Paste.
- Search.
- Size.
- File metadata.
- Chmod.
- Archive.
- Extract.

### Safety behavior

- Operations are scoped to the platform root.
- Path traversal is guarded by root resolution.
- Ownership helpers keep created files aligned with the platform user.

## Logs

### Platform logs

- List available log files.
- Read selected log files.
- Tail log content by line count.
- Separate website and app log routes.

### Service logs

- Admin service log routes read logs for managed services.
- Runtime app logs can include stdout and stderr from process managers.

### Panel logs

- Settings exposes panel log files.
- Audit and activity data provide structured log history.

## Cron Jobs

### Per-platform cron

- Creates cron records in `CronJob`.
- Validates cron schedule input.
- Generates wrapper scripts under managed storage.
- Writes entries into `/etc/cron.d`.
- Runs jobs as the platform user.
- Loads platform runtime environment.

### Delete behavior

- Removes the cron record.
- Removes or refreshes generated cron entries.
- Audits deletion.

## Varnish Cache

### Platform Varnish settings

- Stores cache settings in `VarnishConfig`.
- Supports cache enable/disable per website.
- Supports excluded paths.
- Supports excluded query parameters.
- Supports cache tag prefix.

### VCL management

- Writes per-website VCL fragments.
- Generates aggregate VCL include.
- Validates VCL.
- Reloads Varnish.

### Cache purge

- Purges or bans platform cache entries when config changes or is deleted.
- Uses host matching for platform domains.

## Security Controls

### IP blocking

- Adds platform-level IP block records.
- Deletes IP block records.
- Applies generated security config through platform routing.

### Bot blocking

- Adds user-agent or bot block records.
- Deletes bot block records.
- Applies generated security config through platform routing.

### Basic Auth

- Enables or disables Basic Auth per platform.
- Stores Basic Auth configuration in `BasicAuth`.
- Updates htpasswd-style managed files where configured.

### Cloudflare

- Stores Cloudflare mode/config per platform.
- Supports panel form update for Cloudflare behavior.

### Panel security settings

- Panel Basic Auth.
- Panel IP allowlist.
- Panel IP denylist.
- Panel user-agent denylist.
- Robots blocking.
- Security setting validation to avoid self-lockout.

## Firewall Management

### Supported backends

- ufw.
- firewalld.
- iptables.

### Rule workflows

- Create panel firewall rule.
- Update panel firewall rule.
- Delete panel firewall rule.
- Apply rule to detected backend.
- Parse host firewall status for display.

### Rule model

Firewall rules are stored in `PanelFirewallRule` with:

- Port.
- Protocol.
- Source.
- Action.
- Enabled state.
- Notes and timestamps.

## Managed Services

### Service catalog

The service catalog includes host services such as:

- Nginx.
- MariaDB.
- PostgreSQL.
- Redis.
- Varnish.
- RabbitMQ.
- Docker.
- PHP-FPM versions.

### Service actions

- Create/track service.
- Update metadata.
- Delete tracked service.
- Start.
- Stop.
- Restart.
- Reload where supported.
- Enable.
- Disable.
- Logs.

### Package integration

- Detects package manager.
- Installs known packages when requested.
- Removes known packages when requested.
- Resolves service unit names.
- Detects installed state.

## Settings

### General settings

- Panel name.
- Base URL.
- Public URL behavior.
- Panel custom domain.
- Timezone.
- Update check behavior.
- Backup and path-related settings.

### Security settings

- Panel Basic Auth.
- Panel IP allowlist.
- Panel IP denylist.
- User-agent denylist.
- Robots behavior.
- Login/session security defaults.

### User management

- Create users.
- Update users.
- Delete users.
- Assign roles.
- Assign platform access.
- Protect current or critical users from unsafe deletion.

### Runtime versions

- Add runtime version.
- Remove runtime version.
- Set default runtime version.
- Show installed versions.
- Show available versions.
- Refresh PHP package repositories.

### Services tab

- Manage system services from settings.
- Display service catalog groups.
- Run service actions.
- View service logs.

### Port Info tab

- Admin-only managed runtime port visibility.
- Port availability search.
- Paginated runtime service resource table.

### Event and panel logs

- Show audit and activity records.
- Read selected panel log files.

## Updates

### Release check

- Checks current version.
- Checks latest version from release metadata.
- Persists check state.
- Shows footer update status.

### Install update

- Starts an update job.
- Uses a systemd-run style background update when configured.
- Persists update job state.
- Exposes job status.
- Exposes log tail for update progress.

### Update resilience

- Handles in-progress update state.
- Reads status from state files.
- Keeps UI status separate from immediate process output.

## CLI Commands

| Command | Purpose |
| --- | --- |
| `serve` | Starts the web panel. |
| `runtime restart --platform` | Restarts a platform runtime service. |
| `deploy --platform --branch` | Runs deploy flow for a platform. |
| `health-check --platform` | Runs platform health check from CLI. |
| `bootstrap-host` | Creates or repairs managed host resources. |
| `teardown-managed` | Removes DeployCP-managed host resources. |
| `verify-host` | Runs host preflight verification. |
| `reconcile-managed` | Re-applies managed host resources from database state. |

## Linux Scripts

### Installer scripts

| Script | Purpose |
| --- | --- |
| `install-remote.sh` | Downloads latest or selected release, verifies architecture/checksum, starts install or update. |
| `install.sh` | Installs binary, dependencies, service, environment, and required paths. |
| `setup.sh` | Local setup helper for panel installation flow. |

### Update scripts

| Script | Purpose |
| --- | --- |
| `update.sh` | Updates DeployCP, packages, Adminer assets, verification, and reconcile steps. |
| `self-update.sh` | Runs update as a background system job and writes status/log files. |
| `build-release.sh` | Builds release artifacts. |

### Host operation scripts

| Script | Purpose |
| --- | --- |
| `harden-host.sh` | Applies hardening, fail2ban, logrotate, fallback config, and backup scheduling. |
| `backup.sh` | Creates host-level backups with retention and optional site/log inclusion. |
| `runtime-manager.sh` | Installs, removes, defaults, and lists managed runtime versions. |
| `verify-host.sh` | Runs host verification wrapper. |
| `reconcile.sh` | Runs host reconcile wrapper. |
| `uninstall.sh` | Removes panel-managed installation resources. |
| `tests.sh` | Runs production smoke/integration checks against a real panel instance. |
| `run-adminer.sh` | Runs Adminer helper service. |

## Data Model Coverage

| Model | Feature area |
| --- | --- |
| `User` | Panel users and authentication. |
| `UserPlatformAccess` | Platform-scoped authorization. |
| `AuthSession` | Session tracking. |
| `Website` | Website platform state. |
| `WebsiteDomain` | Extra domains and routing. |
| `SiteUser` | Linux/site user mapping. |
| `GoApp` | Runtime app platform state. |
| `AppEnvVar` | App environment variables. |
| `ManagedService` | Tracked host services. |
| `DatabaseConnection` | MariaDB/PostgreSQL credentials and metadata. |
| `RedisConnection` | Redis connection credentials and metadata. |
| `SSLCertificate` | Certificate status and file paths. |
| `AuditLog` | Security and admin audit trail. |
| `ActivityLog` | Activity feed. |
| `NginxSiteConfig` | Custom Nginx/vhost configuration. |
| `PlatformHealthCheck` | Health check history. |
| `PlatformBackup` | Backup and restore records. |
| `PlatformDeployConfig` | Git deploy configuration and last deploy state. |
| `AlertEvent` | Alert lifecycle and delivery state. |
| `Setting` | Panel settings. |
| `UserPreference` | Per-user preferences such as theme. |
| `PanelFirewallRule` | Firewall rule persistence. |
| `SystemMetricSnapshot` | Historical dashboard metrics. |
| `CronJob` | Platform cron jobs. |
| `VarnishConfig` | Platform Varnish cache settings. |
| `IPBlock` | Platform IP block rules. |
| `BotBlock` | Platform bot block rules. |
| `BasicAuth` | Platform Basic Auth configuration. |
| `CloudflareConfig` | Platform Cloudflare configuration. |
| `FTPUser` | Platform FTP user records. |

## Configuration And Environment

### Application configuration

The panel configuration supports:

- App name, environment, host, and port.
- SQLite database path.
- Storage root.
- Site root.
- Log root.
- Runtime root.
- htpasswd root.
- Backup root.
- Cron root.
- Nginx paths.
- Systemctl and launchctl paths.
- Restricted shell path.
- runuser path.
- Certbot path.
- Firewall command paths.
- Adminer URL.
- Redis timeout.
- Alert webhook.
- Feature flags.
- Database admin credentials.
- ProFTPD settings.
- Redis settings.
- Varnish settings.
- Backup controls.
- Platform mode.

### Dry-run behavior

Dry-run mode redirects many host paths into managed storage so local/test runs can avoid touching live system directories.

## Audit And Activity Logging

### Audit log

- Records actor user ID.
- Records action.
- Records resource and resource ID.
- Records IP address.
- Records structured payload data.

### Activity log

- Stores human-facing activity records.
- Supports dashboard/settings event displays.

### System actions

- Records system actions when there is no normal user actor.
- Useful for reconcile, update, runtime, and host lifecycle events.

## Go-Live Audit Notes

### No database schema risk from current UI-only changes

The recent UI and documentation changes do not require a schema migration. The Port Info feature uses existing runtime app records and host inspection.

### Existing platform runtime compatibility

Existing platforms should keep their runtime behavior unless their runtime settings are saved or a reconcile/reconfigure action is run. The runtime services are generated from stored platform settings.

### PM2 path safety

PM2 must keep service state and working directory separate:

- Working directory should remain the app directory under `htdocs`.
- Platform home should remain the parent container for metadata.
- Runtime state should live in DeployCP-managed runtime state paths.
- Deploy commands must not copy or move the application out of `htdocs`.

### Health check expectations

Apps without a health endpoint can return `404`. That does not mean the service is down, but it will mark the configured health check as critical if the configured health path expects `2xx`.

Recommended choices:

- Set health path to an endpoint that returns `200`.
- Add a small app-level health endpoint.
- Keep the check disabled or adjust expectations when no health endpoint exists.

### Next.js standalone asset safety

For Next.js standalone builds:

- Do not copy `public` into `public/public`.
- Do not copy `.next/static` into `.next/standalone/.next/static/static`.
- Use `cp -R source/. destination/` to copy contents.
- Remove nested bad copies before copying fresh assets.
- Avoid shell globs such as `static?`; `?` is a wildcard and can match unintended names.

### Delete platform cleanup

Delete flows are intended to clean managed platform resources. After deleting a platform, a manual go-live check should confirm:

- Site directory state under `/home/deploycp/platforms/sites/<domain>`.
- Backup directory entries.
- systemd units.
- Nginx sites-enabled entries.
- Nginx sites-available entries.
- Cron entries.
- Redis/process manager leftovers.
- FTP users if the platform had FTP.

## Go-Live Verification Checklist

Use this checklist before production release:

| Check | Expected result |
| --- | --- |
| `git diff --check` | No whitespace or patch formatting errors. |
| `GOCACHE=$(pwd)/storage/cache/.gocache go test ./...` | All Go packages pass. |
| Login flow | Setup/login/logout still work. |
| Settings save | General, security, users, runtime versions, services, and port info tabs render. |
| Platform create | New platform remains under correct `htdocs` root. |
| Runtime create | systemd, PM2, gunicorn, and uWSGI do not alter website root paths. |
| Deploy now | Saves latest deploy config before deploying if configured in UI. |
| Health check | Manual check records a bounded history row. |
| Port info | Shows only managed runtime ports, with pagination and availability search. |
| Delete platform | Removes platform-specific runtime, Nginx, backup, cron, Redis, and FTP artifacts. |
| Reconcile | Rebuilds managed resources without moving application code. |
| Browser render | Visually inspect settings, platform operations, and platform structure sections. |

## Residual Audit Limits

This document is based on static code inspection plus repository-level verification commands. It does not prove that every host command succeeds on every Linux distribution because those paths depend on live host packages, permissions, systemd, firewall backend, PHP repositories, DNS, ACME reachability, and user-provided deploy scripts.

For production, the strongest final validation is:

- Run the Go test suite.
- Run host verification on the target server.
- Create a test platform.
- Attach a runtime.
- Deploy once.
- Run a health check.
- Delete the test platform.
- Confirm no managed leftovers remain.
