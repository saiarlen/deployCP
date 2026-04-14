# Database Migrations

DeployCP currently does **not** execute SQL migration files from this folder.

Schema creation and updates are handled in application bootstrap via:
- `internal/bootstrap/database.go` (`migrate()` + `AutoMigrate`)
- model definitions in `internal/models/models.go`

For fresh installs, no additional appended SQL migrations are required.
