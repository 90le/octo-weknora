#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
output_dir="${1:-$repo_root/artifacts/browserskill}"
# Optional Docker-style target; native builds default to the host platform.
target_platform="${2:-}"
case "$target_platform" in
  ""|linux/amd64|linux/arm64|darwin/amd64|darwin/arm64) ;;
  *) echo "Unsupported BrowserSkill target: $target_platform" >&2; exit 1 ;;
esac
# Build both ends from the same pinned protocol baseline.
source_commit="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["extension"]["source_commit"])' "$repo_root/scripts/browserskill-release.json")"
extension_version="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["extension"]["version"])' "$repo_root/scripts/browserskill-release.json")"
# Native builds only. Docker runs this stage on TARGETPLATFORM so C/Rust
# dependencies and the produced daemon match the final image architecture.
host_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
host_arch="$(uname -m)"
case "$host_arch" in arm64|aarch64) host_arch=arm64 ;; x86_64) host_arch=amd64 ;; esac
if [ -n "$target_platform" ] && [ "$target_platform" != "$host_os/$host_arch" ]; then
  echo "Run the BrowserSkill build on $target_platform (host is $host_os/$host_arch)" >&2
  exit 1
fi
if ! command -v cargo >/dev/null && [ -x "${CARGO_HOME:-$HOME/.cargo}/bin/cargo" ]; then
  export PATH="${CARGO_HOME:-$HOME/.cargo}/bin:$PATH"
fi
command -v cargo >/dev/null || { echo "Rust/Cargo is required to build the matching BrowserSkill daemon" >&2; exit 1; }
daemon_commit="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["daemon"]["source_commit"])' "$repo_root/scripts/browserskill-release.json")"
[ "$daemon_commit" = "$source_commit" ] || { echo "BrowserSkill daemon/extension baseline mismatch" >&2; exit 1; }
mkdir -p "$output_dir"
output_dir="$(cd "$output_dir" && pwd)"
build_dir="$(mktemp -d /tmp/weknora-bsk-build.XXXXXX)"
trap 'rm -rf "$build_dir"' EXIT

git clone --no-checkout https://github.com/Tencent/BrowserSkill.git "$build_dir/source"
git -C "$build_dir/source" checkout --detach "$source_commit"
(
  cd "$build_dir/source"
  npx --yes pnpm@10.17.0 install --frozen-lockfile
  npx --yes pnpm@10.17.0 ext:build:zip
)
extension_archive="$build_dir/source/apps/extension/dist/browser-skillextension-${extension_version}-chrome.zip"
# The extension is also downloaded by itself, so it needs the upstream license
# inside the archive as well as alongside the separately shipped daemon.
python3 - "$extension_archive" "$build_dir/source/LICENSE" <<'PY'
import sys
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile

archive_path, license_path = map(Path, sys.argv[1:])
license_text = license_path.read_bytes()
if not license_text.strip():
    raise SystemExit("BrowserSkill source license is empty")

with ZipFile(archive_path) as archive:
    entries = archive.namelist()
    if "manifest.json" not in entries:
        raise SystemExit("BrowserSkill extension manifest is missing")
    if "LICENSE" in entries and (
        entries.count("LICENSE") != 1 or archive.read("LICENSE") != license_text
    ):
        raise SystemExit("BrowserSkill extension license differs from its source")

if "LICENSE" not in entries:
    with ZipFile(archive_path, "a", compression=ZIP_DEFLATED) as archive:
        archive.writestr("LICENSE", license_text)

with ZipFile(archive_path) as archive:
    bad_entry = archive.testzip()
    if bad_entry is not None or archive.read("LICENSE") != license_text:
        raise SystemExit(f"Could not verify the BrowserSkill extension archive: {bad_entry}")
PY
cp "$extension_archive" "$output_dir/browser-skill-weknora-${extension_version}.zip"
cp "$build_dir/source/LICENSE" "$output_dir/BrowserSkill-LICENSE"

# Build the daemon from the same pinned source; upstream has not published a
# matching CLI 0.3.1 binary.
cargo_target_dir="${CARGO_TARGET_DIR:-$build_dir/target}"
mkdir -p "$cargo_target_dir"
cargo_target_dir="$(cd "$cargo_target_dir" && pwd)"
(
  cd "$build_dir/source"
  # Use the installed toolchain; do not let the checkout's moving "stable"
  # override force a network update during each application build.
  RUSTUP_TOOLCHAIN="${RUSTUP_TOOLCHAIN:-stable}" cargo build --locked --release -p bsk --target-dir "$cargo_target_dir"
)
# Replace atomically: overwriting an executing inode can invalidate macOS code pages.
staged_binary="$(mktemp "$output_dir/.bsk-XXXXXX")"
install -m 755 "$cargo_target_dir/release/bsk" "$staged_binary"
mv -f "$staged_binary" "$output_dir/bsk"
echo "BrowserSkill $extension_version extension and daemon: $output_dir"
