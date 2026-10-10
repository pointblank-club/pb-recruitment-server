# How to run?
- Install [air](https://github.com/air-verse/air?tab=readme-ov-file#installation)
- Run `air` command in the terminal
- Server is running on `http://localhost:8080`
- API documentation (Swagger UI): `http://localhost:8080/swagger/index.html`
- Raw JSON Spec: `http://localhost:8080/swagger/doc.json`
- Enable Swagger with `STAGE=dev` or `ENABLE_SWAGGER=true`; otherwise these URLs return 404.

## API Documentation (Swagger)

To regenerate Swagger documentation after modifying route annotations or DTOs:

```bash
make swagger
```

## DB Migrations

- Install [migrate](https://github.com/golang-migrate/migrate)
- Add `DB_ADDR` to `.env`

### Migration Commands

```bash
# Create a new migration
make migration create_users_table

# Run migrations up
make migrate-up

# NOT AVAILABLE ON PROD - Rollback migrations (specify number of steps)
make migrate-down 1

# NOT AVAILABLE ON PROD - Force migration version (use with caution)
make migrate-force 1
```

## Problem authoring

Finish problem edits and deletion before the contest starts. New update and delete
requests return 409 once the contest starts, including after it ends, to preserve
verdicts and leaderboard scores. Finish any in-flight edits before opening the round.
The start time cannot be changed once the contest starts; end-time extensions remain allowed.
Correcting a started contest requires controlled regrading and score reconciliation.

## PostgreSQL tests

Use a disposable database with migrations through `000024` applied. The test creates
and cleans up its own rows. It skips when `TEST_DATABASE_URL` is unset.

```bash
TEST_DATABASE_URL='postgres://user:password@localhost/test_db' go test ./internal/stores -run TestJudgeMCQPostgres -count=1
```
