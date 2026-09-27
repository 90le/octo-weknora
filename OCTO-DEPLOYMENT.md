# octo-weknora deployment contract

This is the deployment contract, not a live release snapshot. The active revision, cutover result and acceptance evidence are maintained only in [OCTO-STATUS.md](OCTO-STATUS.md).

The deployment uses app and frontend images built from this repository. Releases update the existing WeKnora application; they do not introduce a second management console or a second knowledge database.

## Release identity

- Build both images from one clean, tested commit.
- Tag images `local/octo-weknora-app:<commit>` and `local/octo-weknora-ui:<commit>`.
- Use the upstream Dockerfiles; preserve the app's anydoc parser build rather than disabling it for a faster build.
- Inject the commit/version build arguments supported by the upstream app Dockerfile. Record image IDs and source commit in the private deployment evidence.
- Do not claim the old image was replaced merely by retagging it.

If an external dependency download blocks a full build, an overlay on a previously verified runtime image is an exception, not the default recipe. Use it only when the reviewed source diff requires no new runtime dependency or asset. Recompile the release binary, build the matching UI, verify inherited runtime asset hashes and the actual image IDs, and record the base image, build inputs, limits, isolated startup and parser checks in private release evidence. A dependency, parser, OS-package or runtime-configuration change requires a complete compatible runtime build and corresponding regression checks before switching production.

## Replacement boundary

Update app and frontend image references in the existing deployment. Preserve existing volume mounts, network, secrets, embedding adapter, PostgreSQL and document parser settings unless a reviewed compatibility change requires otherwise. Do not use `docker compose down -v`.

The retired standalone console stays retired. The public Octo knowledge Bot is received by native WeKnora IM only. Other Bot/runtime services are separate and unchanged. The knowledge service does not read their configuration, credentials or workspaces.

## Persistent storage boundary

Keep application data, source snapshots, generated Git caches, parsing temporary files, build caches and private rollout evidence on a persistent data volume. The app's temporary directory must resolve inside its persistent application-data mount before a release is accepted.

Do not migrate a container runtime merely because a larger disk is available. Its target filesystem must support the runtime's overlay storage requirements. If it does not, keep the runtime on its verified filesystem, control root-disk growth with bounded logs and caches, and wait for a compatible replacement volume. Never reformat an active data volume as part of a release.

Check the target filesystem's overlay compatibility before changing containerd storage. Preserve the running and rollback images; investigate measured cache and log usage before any targeted cleanup. Avoid broad image or volume pruning during a release or source recovery.

Before a full image build, prefer at least 20 GiB free on the filesystem that actually holds containerd snapshots; stop the build if free space falls below 10 GiB. Stage source archives, Go/npm caches and temporary compilation on the data volume. After acceptance, retain the running images and one verified rollback pair, then remove only identified unused build cache and older image tags. Use `df` to verify physical recovery: Docker's logical “reclaimable” total is not the amount the root filesystem will necessarily gain.

## Required checks before switching

1. Focused draft visibility tests plus upstream required checks complete successfully.
2. Compare deployed commit and release commit, including migrations and configuration defaults. Review existing local patches. Drain running source sync and parsing jobs, and check `pending/processing/finalizing` knowledge before replacing the app.
3. Create a consistent database backup and corresponding file snapshot; record the exact old images and Compose configuration privately. Record which stores and volumes the recovery point actually covers, including whether Milvus and Neo4j are included. A PostgreSQL dump plus application files is not a complete recovery point for stores it omits.
4. Test the release against an isolated restored database before its first deployment. Avoid connecting a second test application to the production database.
5. Build both images from the reviewed commit. Keep the original image IDs available.

## Acceptance after switching

- App/UI health and authenticated knowledge listing work.
- Existing documents, KB identities, model configuration and bindings remain available.
- One authorized Octo question returns evidence from the expected KB.
- An unpublished draft is absent from user search.
- A query outside the caller's scope is rejected.
- A representative document format still parses through the configured engine.

Do not equate HTTP 200, a passing test suite or an isolated candidate with full integration acceptance. Record which native Bot flows, configuration operations, parsers and failure cases were actually exercised on the released revision. Keep untested combinations explicit in OCTO-STATUS.md.

## Recovery

If no incompatible migration ran, restore the prior image references and verify readback. If migrations changed the schema incompatibly, an image rollback alone is insufficient: restore the reviewed database/file recovery point using the deployment runbook. Check the recovery point's exact store and volume coverage, and record any writes after it before restoring.

Private paths, credentials and deployment snapshots do not belong in this public repository.
