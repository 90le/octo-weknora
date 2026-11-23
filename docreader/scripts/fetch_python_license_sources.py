"""Stage corresponding Python source archives pinned by uv.lock."""

import hashlib
import sys
from pathlib import Path
from urllib.parse import urlsplit
from urllib.request import urlopen


SOURCE_PACKAGES = {"certifi", "chardet", "ebooklib", "tld"}


def fetch_sources(lockfile: Path, destination: Path) -> None:
    try:
        import tomllib
    except ModuleNotFoundError:
        # The Python 3.10 builder already includes pip and its TOML reader.
        from pip._vendor import tomli as tomllib

    with lockfile.open("rb") as lock:
        packages = tomllib.load(lock)["package"]

    destination.mkdir(parents=True, exist_ok=True)
    for package in sorted(SOURCE_PACKAGES):
        matches = [entry for entry in packages if entry["name"] == package]
        if not matches or any("sdist" not in entry for entry in matches):
            raise SystemExit(f"Missing locked source archive for {package}")
        for entry in matches:
            source = entry["sdist"]
            expected_name = f"{package}-{entry['version']}.tar.gz"
            if Path(urlsplit(source["url"]).path).name != expected_name:
                raise SystemExit(f"Unexpected locked source filename for {expected_name}")
            with urlopen(source["url"], timeout=60) as response:
                archive = response.read()
            actual_hash = f"sha256:{hashlib.sha256(archive).hexdigest()}"
            if actual_hash != source["hash"]:
                raise SystemExit(f"Source checksum mismatch for {expected_name}")
            (destination / expected_name).write_bytes(archive)


if __name__ == "__main__":
    fetch_sources(Path(sys.argv[1]), Path(sys.argv[2]))
