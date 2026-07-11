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
push current branch to fork
    |
    v
remote fixed git worktree fetches exact commit
    |
    v
docker build image with BuildKit cache
    |
    v
production backup -> latest symlink
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
4. Confirm the local commit to deploy:
   - Deploy from a pushed commit only.
   - Record the full 40-character commit SHA locally.
   - Use short SHA only for display and image tag naming.

## Remote Git Worktree

Production builds should happen from a fixed git worktree on the server, not from an uploaded archive. The default source directory is `/opt/sub2api-src`; the Docker Compose deployment directory remains separate and keeps the live data, `.env`, compose file, and backups.

One-time setup or migration rules:

- Create or reuse only a dedicated source directory such as `/opt/sub2api-src`.
- Put a marker file named `.sub2api-deploy-worktree` in that directory.
- The source directory must not contain production compose files, backups, `.env`, `data`, `postgres_data`, or `redis_data`.
- Set `origin` to the user's fork, currently `https://github.com/baiyucraft/sub2api.git`.
- Keep the deployment compose directory separate from the source worktree.

Before every production build, verify all safety boundaries:

- The current directory is the expected source path.
- `.sub2api-deploy-worktree` exists.
- `git remote get-url origin` matches the expected fork.
- The worktree has no local tracked changes before resetting, and no untracked files except `.sub2api-deploy-worktree`.
- `.dockerignore` exists and excludes local-only paths such as `.git`, `.tmp`, `.ssh.local`, `node_modules`, build output, and deploy secrets.

Fetch and deploy by exact commit:

```bash
cd /opt/sub2api-src
git fetch origin main
git status --porcelain=v1 --untracked-files=all
git cat-file -e <full-commit-sha>^{commit}
git merge-base --is-ancestor <full-commit-sha> origin/main
git reset --hard <full-commit-sha>
git clean -fdx -e .sub2api-deploy-worktree
git rev-parse HEAD
```

Stop if `git rev-parse HEAD` does not equal the full commit SHA selected locally. Never deploy a floating `origin/main` without checking the exact commit.

## Production Backup

Only keep the latest successful backup, but do not replace it until the new backup has been fully written and verified.

Before changing compose, create a timestamped backup directory under the deploy directory, for example `backups/<timestamp>-<short-sha>/`. Include:

- `docker-compose.yml`
- `.env` when present
- current image/container information
- a PostgreSQL `dumpall`
- sha256 checksums for backup artifacts
- a CSV snapshot of normalized upstream-bound accounts matching `type = 'apikey' AND upstream_config_id IS NOT NULL AND upstream_key_id IS NOT NULL`, including `id`, `name`, `concurrency`, `load_factor`, `rate_multiplier`, `priority`, `upstream_config_id`, and `upstream_key_id`

If the database dump or checksum generation fails, stop before deployment and keep the existing `backups/latest` unchanged.

After the new backup is complete, atomically update `backups/latest` as a symlink to the new backup directory, then delete older backup directories. Do not delete the old latest backup before the new backup has passed checks.

Prefer a temporary symlink plus rename:

```bash
cd <deploy-dir>
ln -sfn "<timestamp>-<short-sha>" backups/latest.tmp
mv -T backups/latest.tmp backups/latest
```

## Build Image

Build from the fixed remote git worktree with BuildKit enabled. The Dockerfile uses cache mounts for the pnpm store, Go module cache, and Go build cache, so repeated builds should avoid re-downloading most dependencies.

Prefer:

```bash
cd /opt/sub2api-src
export DOCKER_BUILDKIT=1
docker build --network=host --progress=plain \
  --build-arg COMMIT=<short-sha> \
  --build-arg VERSION=<base-version>-baiyu \
  --build-arg DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t sub2api:baiyu-<base-version>-<short-sha> \
  -t sub2api:<base-version>-baiyu .
```

Use `<base-version>` from `backend/cmd/server/VERSION`. The runtime/application version must stay simple (`<base-version>-baiyu`, for example `0.1.142-baiyu`). The compose image line should use the unique `sub2api:baiyu-<base-version>-<short-sha>` tag; the shorter `sub2api:<base-version>-baiyu` tag is only a convenience alias.

Use `--network=host` because Alpine and package downloads can hang under the default build network in this environment. Capture logs to `/tmp/sub2api-build-<timestamp>.log` and write the exit code to a `.rc` file for polling.

After the build finishes, verify the new image before touching compose:

```bash
docker image inspect sub2api:baiyu-<base-version>-<short-sha>
```

Record the new image ID and the previous running image ID. If image inspection fails, stop and leave production untouched.

If an earlier build hangs before production is changed, stop that build process and retry with `--network=host --progress=plain`.

## Switch Service

Only after the image exists:

1. Make or confirm the latest backup described above.
2. Replace the `sub2api` service image line with the new local image tag.
3. Run:

```bash
cd <deploy-dir>
docker compose up -d sub2api
```

Do not restart Postgres or Redis unless required.
Do not run `docker image prune` during deployment. Keep at least the previous image available for fast compose-only rollback.

## Verify

Run these checks after `docker compose up -d sub2api`:

- `docker ps` shows `sub2api` using the new image and `healthy`.
- `curl -i http://127.0.0.1:<mapped-port>/health` returns `200`.
- `docker compose logs --tail=120 sub2api` has no new startup panic or repeated Sub2API sync errors.
- For Sub2API upstream sync deployments, query the target account count and sync fields from Postgres without printing credentials.

## Rollback

Rollback is compose-only when no data migration is involved:

```bash
cd <deploy-dir>
# restore the previous image line or restore backups/latest/docker-compose.yml
docker compose up -d sub2api
```

If a sync deployment changed account multipliers incorrectly, use the backed up target account CSV and database dump to decide whether to restore only affected account fields or perform a broader database restore.

## Final Report

Report:

- new image tag and image id
- exact deployed commit SHA
- source worktree path
- backup directory path and `backups/latest` target
- health and auth-check results
- whether target Sub2API upstream accounts were present
- rollback command summary

Do not include secrets or raw `.ssh.local` contents.
