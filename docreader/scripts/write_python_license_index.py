"""Index notices from the Python packages installed in the DocReader image."""

import json
import sys
from importlib.metadata import distributions
from pathlib import Path

from docreader.scripts.fetch_python_license_sources import SOURCE_PACKAGES


NOTICE_PREFIXES = ("license", "copying", "notice", "thirdpartynotice")
REQUIRE_LICENSE_FILES = SOURCE_PACKAGES | {"numpy", "onnxruntime"}


def write_index(destination: Path, source_directory: Path | None = None) -> None:
    environment = Path(sys.prefix).resolve()
    packages = []

    for package in distributions():
        name = package.metadata["Name"]
        files = package.files or ()
        license_files = []
        for entry in files:
            filename = entry.name.lower().replace("-", "").replace("_", "")
            if not filename.startswith(NOTICE_PREFIXES) or entry.suffix in {".py", ".pyc"}:
                continue

            notice = entry.locate().resolve()
            if notice.is_relative_to(environment) and notice.is_file() and notice.stat().st_size:
                license_files.append(notice.relative_to(environment).as_posix())

        normalized_name = name.lower()
        if normalized_name in REQUIRE_LICENSE_FILES and not license_files:
            raise SystemExit(f"Missing installed license files for {name} {package.version}")

        package_info = {
            "name": name,
            "version": package.version,
            "license_files": sorted(license_files),
        }
        if source_directory is not None and normalized_name in SOURCE_PACKAGES:
            source_archive = source_directory / f"{normalized_name}-{package.version}.tar.gz"
            if not source_archive.is_file() or not source_archive.stat().st_size:
                raise SystemExit(f"Missing corresponding source archive for {name} {package.version}")
            package_info["corresponding_source"] = source_archive.name
        packages.append(package_info)

    if not packages:
        raise SystemExit("No installed Python packages to include in the license index")

    packages.sort(key=lambda package: package["name"].lower())
    destination.write_text(json.dumps(packages, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    write_index(Path(sys.argv[1]), Path(sys.argv[2]) if len(sys.argv) > 2 else None)
