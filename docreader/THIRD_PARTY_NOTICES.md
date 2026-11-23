# DocReader third-party notices

The DocReader application is part of WeKnora. Its copyright and MIT license
are in `/app/LICENSE`.

The image preserves Python wheels' original notices and metadata in
`/app/.venv`. The generated `/app/licenses/python-packages.json` indexes
the installed versions and their notice files. Paths in the index are
relative to `/app/.venv`. Debian packages retain their copyright files
under `/usr/share/doc`, and Playwright's WebKit download retains the
notices included in the browser distribution.

- **EbookLib 0.20** is used by the EPUB parser under the GNU Affero General
  Public License v3.0 or later. Its license is included in the wheel and
  indexed in `python-packages.json`.
  [Versioned upstream source and license](https://github.com/aerkalov/ebooklib/tree/v0.20).
  The image also includes the SHA-256-verified source archive in
  `/app/licenses/sources/ebooklib-0.20.tar.gz`.
- **Certifi**, **chardet**, and **tld** retain their wheel notices and their
  SHA-256-verified source archives under `/app/licenses/sources/`. The index
  lists the archive filenames for their installed versions.
- **gRPC Health Probe** is shipped as the container health-check executable.
  [Apache-2.0 license](licenses/grpc-health-probe-v0.4.24/LICENSE) ·
  [Versioned upstream source](https://github.com/grpc-ecosystem/grpc-health-probe/tree/v0.4.24)
- **Protocol Buffers compiler** is installed from the upstream release.
  [BSD-3-Clause license](licenses/protoc-3.19.4/LICENSE) ·
  [Versioned upstream source](https://github.com/protocolbuffers/protobuf/tree/v3.19.4)

When upgrading either executable or EbookLib, update its versioned source
link. When upgrading an executable, update its corresponding upstream
license file. When upgrading Python or system packages, review the
licenses of the versions included in the resulting image before
distribution.
