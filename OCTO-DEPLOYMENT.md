# octo-weknora deployment contract

This is the deployment contract, not a live release snapshot. The active revision, cutover result and acceptance evidence are maintained only in [OCTO-STATUS.md](OCTO-STATUS.md).

The deployment uses app and frontend images built from this repository. Releases update the existing WeKnora application; they do not introduce a second management console or a second knowledge database.

## Release identity

- Build both images from one clean, tested commit.
- Tag images `local/octo-weknora-app:<commit>` and `local/octo-weknora-ui:<commit>`.
- Use the upstream Dockerfiles; preserve the app's anydoc parser build rather than disabling it for a faster build.
- Inject the commit/version build arguments supported by the upstream app Dockerfile. Record image IDs and source commit in the private deployment evidence.
- Do not claim the old image was replaced merely by retagging it.

## Replacement boundary

Update app and frontend image references in the existing deployment. Preserve existing volume mounts, network, secrets, embedding adapter, PostgreSQL and document parser settings unless a reviewed compatibility change requires otherwise. Do not use `docker compose down -v`.

The retired standalone console stays retired. The public Octo knowledge Bot is received by native WeKnora IM only. Other Bot/runtime services are separate and unchanged. The knowledge service does not read their configuration, credentials or workspaces.

## Required checks before switching

1. Focused draft visibility tests plus upstream required checks complete successfully.
2. Compare deployed commit and release commit, including migrations and configuration defaults. Review existing local patches.
3. Create a consistent database backup and corresponding file snapshot; record the exact old images and Compose configuration privately.
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

If no incompatible migration ran, restore the prior image references and verify readback. If migrations changed the schema incompatibly, an image rollback alone is insufficient: restore the reviewed database/file recovery point using the deployment runbook. Record any writes after the recovery point before restoring.

Private paths, credentials and deployment snapshots do not belong in this public repository.
