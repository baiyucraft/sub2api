---
name: sub2api-production-deploy
description: Safely deploy this Sub2API fork to the user's production Docker Compose environment. Use when asked to deploy, release, roll out, verify, or roll back this repository's production service using the local .ssh.local connection file, Docker image builds, compose updates, database backups, health checks, and Sub2API-specific endpoint verification.
---

# Sub2API Production Deploy

Use this checklist for production deployments of this repository. Keep user-facing updates in Chinese.

## Safety Rules

- Read `.ssh.local` only as local connection input. Never print, quote, copy, commit, or log its host, user, password, private key, or token values.
- Give a concrete deployment plan and get user confirmation before changing production state.
- Keep the existing service running until a new image is built successfully.
- Do not modify production `docker-compose.yml` until backups are complete and the new image exists.
- Restart only the `sub2api` service unless the user explicitly asks for broader maintenance.
- Redact secrets in logs and command output before relaying anything to the user.
- If a build or validation fails before compose is changed, leave production untouched and report the failure.

## Flow

```text
local verify
    |
    v
package workspace -> upload to /tmp
    |
    v
production backup
    |
    v
docker build image
    |
    v
switch compose image -> docker compose up -d sub2api
    |
    v
health, auth, logs, rollback notes
```

## Preflight

1. Check local status and avoid reverting unrelated user changes.
2. Run focused tests for the change. For this project, typical checks are:
   - `go test ./internal/server/middleware ./internal/handler ./internal/service -run "<focused pattern>"`
   - `./node_modules/.bin/vue-tsc --noEmit` from `frontend/` when frontend changed.
   - `git diff --check`.
3. Confirm the production deployment mode:
   - Use `.ssh.local` with a script or SSH client without echoing secrets.
   - Expect Docker Compose under a production deploy directory.
   - Confirm current containers, current image, port mapping, and service health.

## Package And Upload

Create a clean archive from the workspace, excluding bulky or local-only paths such as `.git`, `.tmp`, `node_modules`, build output, and local secrets. Put archives under `.tmp/` locally and `/tmp/` remotely.

Record the local and remote sha256. If the checksum differs, stop.

## Production Backup

Before changing compose, create a timestamped backup directory under the deploy directory. Include:

- `docker-compose.yml`
- `.env` when present
- current image/container information
- a PostgreSQL `dumpall`
- sha256 checksums for backup artifacts
- a CSV snapshot of `type=apikey` accounts whose `extra.upstream_provider = 'sub2api'`, including `id`, `name`, `rate_multiplier`, `priority`, and sync-related `extra` fields

If the database dump fails, stop before deployment.

## Build Image

Build in a fresh `/tmp/sub2api-build-<timestamp>` directory from the uploaded archive.

Prefer:

```bash
docker build --network=host --progress=plain \
  --build-arg COMMIT=<short-sha> \
  --build-arg VERSION=<base-version>-codex.<timestamp> \
  --build-arg DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t sub2api:codex-<short-sha>-<timestamp> .
```

Use `--network=host` because Alpine and package downloads can hang under the default build network in this environment. Capture logs to `/tmp/sub2api-build-<timestamp>.log` and write the exit code to a `.rc` file for polling.

If an earlier build hangs before production is changed, stop that build process and retry with `--network=host --progress=plain`.

## Switch Service

Only after the image exists:

1. Make a second quick backup of `docker-compose.yml`.
2. Replace the `sub2api` service image line with the new local image tag.
3. Run:

```bash
cd <deploy-dir>
docker compose up -d sub2api
```

Do not restart Postgres or Redis unless required.

## Verify

Run these checks after `docker compose up -d sub2api`:

- `docker ps` shows `sub2api` using the new image and `healthy`.
- `curl -i http://127.0.0.1:<mapped-port>/health` returns `200`.
- `curl -i http://127.0.0.1:<mapped-port>/v1/sub2api/account-meta` without authorization returns `401`.
- `docker compose logs --tail=120 sub2api` has no new startup panic or repeated Sub2API sync errors.
- For Sub2API upstream sync deployments, query the target account count and sync fields from Postgres without printing credentials.

## Rollback

Rollback is compose-only when no data migration is involved:

```bash
cd <deploy-dir>
# restore the previous image line or restore the backed up docker-compose.yml
docker compose up -d sub2api
```

If a sync deployment changed account multipliers incorrectly, use the backed up target account CSV and database dump to decide whether to restore only affected account fields or perform a broader database restore.

## Final Report

Report:

- new image tag and image id
- backup directory path
- health and auth-check results
- whether target Sub2API upstream accounts were present
- rollback command summary

Do not include secrets or raw `.ssh.local` contents.
