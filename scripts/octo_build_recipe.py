"""Prepare the network-only Dockerfile override used by the Octo deployment.

Does not modify application source, disable anydoc, or deploy containers.
APT signatures and Go checksum verification remain enabled.
"""
import argparse
import hashlib
import json
from pathlib import Path


def render(source: str) -> str:
    apt = "sed -i 's|http://deb.debian.org|http://mirrors.tuna.tsinghua.edu.cn|g' /etc/apt/sources.list.d/debian.sources && apt-get update"
    marker = "ENV RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo"
    rust = marker + '\nENV RUSTUP_DIST_SERVER=https://rsproxy.cn RUSTUP_UPDATE_ROOT=https://rsproxy.cn/rustup\nRUN mkdir -p /usr/local/cargo && printf \'[source.crates-io]\\nreplace-with = "rsproxy-sparse"\\n[source.rsproxy-sparse]\\nregistry = "sparse+https://rsproxy.cn/index/"\\n\' > /usr/local/cargo/config.toml'
    if source.count(marker) != 1 or "apt-get update" not in source:
        raise ValueError("Upstream Dockerfile changed; review recipe before building")
    return source.replace("apt-get update", apt).replace(marker, rust)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, default=Path(__file__).resolve().parents[1] / "docker/Dockerfile.app")
    parser.add_argument("--output", type=Path, required=True, help="Generated Dockerfile outside the source tree")
    args = parser.parse_args()
    source = args.source.resolve()
    output = args.output.resolve()
    if output == source:
        parser.error("Output must not overwrite the upstream Dockerfile")
    recipe = render(source.read_text(encoding="utf-8"))
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_bytes(recipe.encode("utf-8"))
    print(json.dumps({"recipe_sha256": hashlib.sha256(recipe.encode()).hexdigest(), "output": str(output)}))


if __name__ == "__main__":
    main()
