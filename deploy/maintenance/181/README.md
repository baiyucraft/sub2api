# Migration 181 production runbook

This directory contains the auditable database operations for the incompatible
upstream-key platform migration. It contains no credentials or environment
addresses.

## Forward deployment

1. Keep the old production application running while building and validating the
   full-SHA candidate image.
2. On the development VM, back up the local database, run migration 181, run the
   repository integration tests against local PostgreSQL, and validate the UI.
3. Before production migration, stop the application container. This freezes
   writes and stops the in-process periodic upstream synchronizer. Verify there
   are no application sessions or long-running write transactions. Stop and
   runtime-mask both `sub2api-backup.timer` and `sub2api-backup.service`; verify
   both are inactive and masked before running the maintenance backup. Stop
   Nginx before any candidate process starts:

   ```bash
   systemctl mask --runtime --now sub2api-backup.timer sub2api-backup.service
   systemctl stop nginx
   docker compose stop -t 30 sub2api
   ```
4. Run `maintenance-backup.sh` with the installed backup recipient and restricted
   upload identity. It creates and verifies the coordinated PostgreSQL, Redis,
   configuration, Compose, image, and encrypted off-site recovery point without
   ever starting the application. Keep the application stopped.
   The restricted receiver currently accepts the `daily` transport class only;
   after upload, verify the exact artifact and checksum on the backup host, then
   run `promote-release-backup.sh` on the backup host with the exact
   `ARTIFACT_NAME` and `ARTIFACT_SHA256` returned by the maintenance script. It
   promotes that immutable encrypted artifact to the release-recovery namespace.
   Require at least 5 GiB free on the backup host. Do not unmask the regular
   backup units until the release finishes or rolls back.
5. Verify the recovery point includes encrypted row snapshots for the affected
   upstream configs, keys, and bound accounts. Record filenames, row counts, and
   SHA-256 only.
6. Run `preflight.sql`, then execute the candidate as a one-shot container with
   `/app/sub2api --migrate-only`. It must exit zero and must not start HTTP, Redis
   workers, schedulers, or background jobs. Verify migration 181 and its checksum
   while the production application remains stopped.

   ```bash
   export COMPOSE_FILE=docker-compose.yml:docker-compose.release.yml
   export SUB2API_RELEASE_IMAGE="$candidate_full_sha_tag"
   BIND_HOST=127.0.0.1 UPSTREAM_SYNC_AUTO_ENABLED=false \
     EXPECTED_IMAGE_ID="$candidate_image_id" EXPECTED_AUTO_SYNC=false \
     ./verify-release-compose.sh
   UPSTREAM_SYNC_AUTO_ENABLED=false \
     docker compose run --rm --no-deps sub2api /app/sub2api --migrate-only
   ```
7. Run `calibrate-dao-name.sql`, then start the production candidate with
   `UPSTREAM_SYNC_AUTO_ENABLED=false`. Manual admin synchronization remains
   available while periodic synchronization is paused. Keep Nginx stopped and
   access the app only through an SSH tunnel to its loopback-bound host port;
   public writes remain frozen.

   ```bash
   COMPOSE_FILE=docker-compose.yml:docker-compose.release.yml \
     SUB2API_RELEASE_IMAGE="$candidate_full_sha_tag" \
     BIND_HOST=127.0.0.1 UPSTREAM_SYNC_AUTO_ENABLED=false \
     docker compose up -d --no-deps --force-recreate sub2api
   ss -ltn | grep -E '127\.0\.0\.1:18080'
   ! ss -ltn | grep -E '(0\.0\.0\.0|\[::\]):18080'
   ```

   From DMIT and an independent external client, require the public `/health`
   request to fail while Nginx is stopped. Through the SSH tunnel, require the
   loopback `/health` request to succeed before any admin operation.
8. Locate the SunAI and Dao configs by their normalized `site_url`. Run the normal
   single-config sync endpoint for each. Do not identify either config by database
   ID in a reusable script.
9. Review the Key Platform dialog. Assign any unresolved, ambiguous, or conflicting
   key manually before creating accounts. Do not create new SunAI, `kiro`, or
   `ccmax` accounts.
10. Run `verify-calibration.sql`. It must pass all ten expected Key/group/platform
    mappings, prove SunAI has no derived accounts, and prove Dao still has exactly
    the existing active, schedulable `刀哥-pro` and `刀哥-plus` accounts.
11. Verify Dao's existing OpenAI accounts remain active and are renamed by the
    normal `upstream-name-key-name` rule. Record the scheduler outbox watermark
    before manual synchronization. After calibration, run
    stop the candidate and run `enqueue-scheduler-validation.sql`; require its new
    Dao-account event ID to exceed the pre-sync maximum and use
    `verify-scheduler-event.sql` to prove its payload exactly targets both Dao
    accounts while the consumer is stopped. Restart the candidate, then require
    Redis to consume that exact verified event using `verify-scheduler-outbox.sql`.
    Inspect `sched:v2:acc:<id>` internally and
    assert the two Dao accounts have the expected name, OpenAI platform, active
    status, and schedulable state without printing cached credentials. Complete
    auth, streaming, and sanitized-log smoke checks through the SSH tunnel.
12. Only after calibration, scheduler propagation, and production smoke checks
    pass, remove `UPSTREAM_SYNC_AUTO_ENABLED=false` and recreate only the app.
    Recheck it internally, then start Nginx and verify direct and proxy paths.
    Finally unmask and restart the normal backup timer:

    ```bash
    COMPOSE_FILE=docker-compose.yml:docker-compose.release.yml \
      SUB2API_RELEASE_IMAGE="$candidate_full_sha_tag" \
      BIND_HOST=127.0.0.1 UPSTREAM_SYNC_AUTO_ENABLED=true \
      docker compose up -d --no-deps --force-recreate sub2api
    systemctl start nginx
    systemctl unmask --runtime sub2api-backup.service sub2api-backup.timer
    systemctl start sub2api-backup.timer
    ```

    Persist the verified `COMPOSE_FILE`, `SUB2API_RELEASE_IMAGE`, and
    `UPSTREAM_SYNC_AUTO_ENABLED=true` values in the deployment `.env` only after
    internal acceptance, then rerun `verify-release-compose.sh` with
    `EXPECTED_AUTO_SYNC=true`. The pre-migration recovery point contains the old
    `.env` and base Compose for coordinated rollback.

## Rollback

Rollback is coordinated, never image-only:

1. Stop the candidate and keep writes frozen.
2. Restore PostgreSQL, Redis, configuration, Compose, and the previous image only
   from the coordinated pre-migration recovery point. Post-migration snapshots and
   `rollback-schema.sql` are not an approved production rollback source because
   they cannot undo data mutations from synchronization. Confirm restored upstream
   platforms contain no `NULL` values.
3. Start the previous image and verify migration state, auth, scheduling, both
   health paths, and sanitized logs. The previous image resumes the exact
   pre-release synchronization behavior restored by the coordinated recovery
   point; it does not understand the new pause setting.
