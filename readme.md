# DeployCP

DeployCP is a self-hosted control panel (Go + Fiber + Jet) for managing platforms, services, users, databases, SSL, and server operations on Linux/macOS.

This file is the main app guide. Detailed architecture and decisions are in the `readme/` folder.

## Docs Map
- `readme/README.md` -> documentation index
- `readme/PROJECT_CONTEXT.md` -> product/runtime context
- `readme/CURRENT_STATE.md` -> current implemented behavior + gaps
- `readme/ARCHITECTURE.md` -> architecture, data flows, route map
- `readme/DECISIONS.md` -> architectural decisions
- `readme/AI_RULES.md` -> contributor/AI guardrails

## What The Application Provides
- Unified **Platforms** flow (`/platforms`) for:
  - website platforms: `static`, `php`
  - runtime platforms: `go`, `python`, `node`, `binary`
- Platform manage UI (single template): runtime controls, databases, SSL, SSH/FTP, logs.
- Settings tabs:
  - General
  - Users
  - Events (paginated)
  - Services
  - Firewall
- Dashboard telemetry and history.
- Auth/session management + login captcha.

## Navigation
- Dashboard: `/`
- Platforms: `/platforms`
- Settings (admin): `/settings`
- Profile: `/profile`

Compatibility routes still exist for `/websites` and `/apps` and redirect/alias into the unified platform flow.

## Architecture (High Level)
DeployCP uses a layered monolith architecture:
1. `middleware`
2. `handlers`
3. `services`
4. `repositories`
5. `models`

Key principles from architecture docs:
- high-risk system operations are orchestrated in services
- OS-specific behavior is isolated in platform adapters
- handlers own HTTP/form flow only

## Tech Stack
- Go module: `deploycp`
- Web framework: Fiber
- ORM: GORM
- DB: SQLite (panel metadata + `fiber_sessions`)
- Templates: Jet (`frontend/templates/*.jet.html`)
- Frontend libs: Tailwind CDN, Notyf, Lucide, Chart.js

## Folder Structure
```text
main.go
frontend/
  assets/
  templates/
internal/
  bootstrap/
  config/
  handlers/
  middleware/
  models/
  platform/
  repositories/
  services/
  system/
  utils/
  validators/
  views/
database/
readme/
scripts/
storage/
```

## Data Model Summary
Primary entities:
- Identity/session: `User`, `AuthSession`
- Access scope: `UserPlatformAccess`
- Platforms: `Website`, `GoApp`, `WebsiteDomain`, `SiteUser`
- Runtime metadata: `AppEnvVar`, `ManagedService`
- Data integrations: `DatabaseConnection`, `RedisConnection`
- Security/TLS: `SSLCertificate`
- Settings/preferences: `Setting`, `UserPreference`, `NginxSiteConfig`
- Audit/telemetry: `AuditLog`, `ActivityLog`, `SystemMetricSnapshot`

Database model is unified on the physical table `platforms` (legacy website/app tables converged).

## Canonical Route Summary
Source of truth: `internal/bootstrap/app.go` -> `registerRoutes`

- Public:
  - `GET /login`
  - `GET /login/captcha`
  - `POST /login`
  - `POST /logout`
  - `POST /theme`
- Secured:
  - `GET /`
  - `GET /dashboard/live`
  - `GET /dashboard/history`
  - `GET /profile`
  - `POST /profile`
  - `GET /profile/password` (redirect to `/profile`)
  - `POST /profile/password`
  - `POST /profile/theme`
- Platform hub:
  - `GET /platforms`
  - `GET /platforms/new`
  - `POST /platforms`
  - `GET /platforms/:ref`
- Services:
  - `GET /services` redirects to `/settings?tab=services`
  - service APIs remain under `POST /services...`
- Logs:
  - `GET /logs` redirects to `/settings?tab=events`

## Security and Access
- Auth/session middleware gates secured routes.
- CSRF is enforced when enabled in config.
- Login rate limit is applied on `POST /login`.
- Role behavior:
  - `admin`: full access
  - `site_manager`: platform access only (no settings/services/logs)
  - `user`: restricted to assigned platforms
- Destructive actions use confirmation modal UX.
- Platform delete requires typed `DELETE` confirmation.

## Platform Modes
- Normal mode: real OS adapter actions.
- `PLATFORM_MODE=dryrun`:
  - safe local simulation
  - no real system/user/nginx mutations
  - privileged paths redirected under `./storage/dryrun`

## Run Locally
1. Configure `.env` (start from `.env.example`).
2. Start app:
```bash
go run main.go
```

## Validation
Use local cache in sandboxed/dev environments:
```bash
GOCACHE=$(pwd)/storage/cache/.gocache go test ./...
GOCACHE=$(pwd)/storage/cache/.gocache go vet ./...
```

## Operational Notes
- Settings timezone (`panel_timezone`) is applied app-wide (`time.Local`).
- Runtime version catalogs in Settings feed create/manage runtime selectors.
- Runtime version deletion is protected: if in use by platforms, removal is blocked.
- Toast classification marks failure phrases (`cannot`, `blocked`, `in use`) as error notifications.

## Primary Files To Change
- Route wiring: `internal/bootstrap/app.go`
- Platform lifecycle: `internal/services/website_service.go`, `internal/services/app_service.go`
- Settings behavior: `internal/handlers/settings_handler.go`, `internal/services/settings_service.go`
- Shared UI shell + confirm/toasts: `frontend/templates/layouts_base.jet.html`
- Core CSS: `frontend/assets/css/app.css`
