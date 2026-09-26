# GitHub repository documents

Native entry: Knowledge base → Settings → Data sources → GitHub.
Provide `owner/repository` (or its GitHub HTTPS URL), optional ref and one
repository-relative file/directory per line. An empty ref uses the default
branch. Public repositories work without a token, subject to GitHub's public
rate limit; private repositories need a read-only Contents token.

Example native settings:

```json
{
  "repository": "90le/octo-weknora",
  "ref": "main",
  "paths": ["README.md", "docs"]
}
```

Credentials use the existing data-source credential subresource and encryption.
They never belong in the settings object or a client-visible response.

The optional `DATASOURCE_GITHUB_SHARED_GIT_CACHE=1` switch reuses one physical
bare Git mirror for sources with the same tenant, canonical repository and
identical credential scope. It requires `SYSTEM_AES_KEY`, Git, and a private
persistent `DATASOURCE_SNAPSHOT_DIR` whose filesystem supports cross-process
file locks. Each source still has its own manifest, index, cursor and access
checks. The existing per-source cache remains available when the switch is
off; turning it on does not migrate or delete old per-source caches. Source
deletion and credential changes remove a shared mirror only after checking
that no other source in the tenant still subscribes. If that check fails, the
mirror is retained for review. This switch does not combine HEAD checks,
schedule runs or API rate budgets; it is not evidence of production rollout.
Each mirror keeps Git's fetched commit refs until it reaches the configured
per-cache size cap (2 GiB by default). Under its exclusive lock, an oversized
mirror is replaced with a fresh shallow mirror for the requested commit. A
concurrent plan that still needs an older object then fails safely and retries
from its fixed commit; published content and cursors stay intact. A single
fresh repository above the cap still needs operator action. Interrupted clone
stages are removed under the same lock on the next acquisition or source
cleanup. There is no global orphan sweeper: after a crash with no subsequent
access, or after installation-key rotation, operators must inspect the private
`github-shared-git-v1` directory with sync workers stopped before removing
unreferenced stages or mirrors.

Each fetch resolves a commit and reads its tree/blobs. The file list is compared
with the last acknowledged manifest. Unchanged blobs are skipped. Returned
documents preserve the repository-relative folder under `owner/repository/`
and use a fixed-commit GitHub URL in native knowledge references.

The connector imports supported document formats only. Source code, hidden
files, dependencies, symlinks and submodules are excluded. Limits are 2000
documents, 16 MiB per file and 64 MiB per batch. A truncated API tree fails
explicitly; it never provides evidence for deletion. Source-code browsing and
server folder connectors are separate, unfinished work.

GitHub imports stage a deterministic document candidate and wait for native
processing to finish before retiring the previous document. Failures retain
the previous version; interrupted candidates can be resumed. Partial failures
do not advance the manifest. This is per-document replacement, not an atomic
whole-KB or cross-source snapshot. Existing connectors are not silently moved
onto this new replacement path.

Deletion propagation is opt-in in the new GitHub UI. Only a complete listing
with the same source selection may report a missing remote file. Changing
filters/ref/repository does not delete previously imported documents; review
and remove obsolete content explicitly. Removing a data source is distinct
from deleting its knowledge base or writing to GitHub.
