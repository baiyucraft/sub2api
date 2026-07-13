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
   are no application sessions or long-running write transactions.
4. Create and verify the coordinated PostgreSQL, Redis, configuration, Compose,
   image, and encrypted off-site recovery point. Keep the application stopped.
5. Export encrypted row snapshots for the affected upstream configs, keys, and
   bound accounts. Record filenames, row counts, and SHA-256 only.
6. Run `preflight.sql`, start only the candidate application, and verify migration
   181 is recorded with the expected checksum.
7. Stop the candidate again before calibration. Run `calibrate-dao-name.sql`, then
   start the candidate.
8. Locate the SunAI and Dao configs by their normalized `site_url`. Run the normal
   single-config sync endpoint for each. Do not identify either config by database
   ID in a reusable script.
9. Review the Key Platform dialog. Assign any unresolved or ambiguous key manually
   before creating accounts. Do not create new SunAI, `kiro`, or `ccmax` accounts.
10. Run `verify-calibration.sql`. It must pass all ten expected Key/group/platform
    mappings, prove SunAI has no derived accounts, and prove Dao still has exactly
    the existing active, schedulable `刀哥-pro` and `刀哥-plus` accounts.
11. Verify Dao's existing OpenAI accounts remain active and are renamed by the
    normal `upstream-name-key-name` rule. Verify scheduler outbox consumption,
    direct and proxy health paths, auth, streaming, and sanitized logs.

## Rollback

Rollback is coordinated, never image-only:

1. Stop the candidate and keep writes frozen.
2. Restore PostgreSQL, Redis, configuration, and Compose from the coordinated
   pre-migration recovery point. Confirm restored upstream platforms contain no
   `NULL` values.
3. If restoring a database snapshot taken after migration 181, run
   `rollback-schema.sql`. It refuses to run while any platform is `NULL` or a
   schedulable account conflicts with its key.
4. Start the previous image and verify migration state, auth, scheduling, both
   health paths, and sanitized logs.
5. Keep NewAPI synchronization paused until a platform-aware candidate is deployed
   again.
