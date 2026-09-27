#!/usr/bin/env python3
"""Offline, evidence-bounded scoring for Octo Agent acceptance observations.

The evaluator never calls WeKnora, Octo, GitHub, or a model. It deliberately
does not infer factual or semantic correctness from an answer or a URL.
"""

from __future__ import annotations

import argparse
import json
import re
import statistics
import sys
from pathlib import Path
from urllib.parse import unquote, urlsplit


SOURCE_KINDS = frozenset({"github_code", "github_release", "kb_document", "wiki_page"})
OUTCOMES = frozenset({"answer", "abstain", "partial"})
SHA40 = re.compile(r"[0-9a-fA-F]{40}\Z")
IDENTIFIER = re.compile(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,79}\Z")
REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\Z")
LINE_FRAGMENT = re.compile(r"L([1-9][0-9]*)(?:-L([1-9][0-9]*))?\Z")
MAX_CITED_LINES = 12


class InvalidInput(ValueError):
    """A case/observation file cannot be interpreted safely."""


def _require_object(value: object, label: str) -> dict:
    if not isinstance(value, dict):
        raise InvalidInput(f"{label}: expected object")
    return value


def _require_list(value: object, label: str) -> list:
    if not isinstance(value, list):
        raise InvalidInput(f"{label}: expected list")
    return value


def _require_text(value: object, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise InvalidInput(f"{label}: expected nonempty string")
    return value


def _require_optional_count(value: object, label: str) -> int | None:
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise InvalidInput(f"{label}: expected nonnegative integer or null")
    return value


def _read_jsonl(path: Path, label: str):
    try:
        with path.open("r", encoding="utf-8") as stream:
            for number, line in enumerate(stream, 1):
                if not line.strip():
                    continue
                try:
                    value = json.loads(line)
                except json.JSONDecodeError as exc:
                    raise InvalidInput(f"{label} line {number}: invalid JSON") from exc
                yield number, _require_object(value, f"{label} line {number}")
    except (OSError, UnicodeError) as exc:
        # Do not print the path or an input fragment: either may be private.
        raise InvalidInput(f"{label}: could not read UTF-8 JSONL") from exc


def _repository(value: object, label: str) -> str:
    repository = _require_text(value, label)
    if not REPOSITORY.fullmatch(repository):
        raise InvalidInput(f"{label}: expected owner/repository")
    return repository.lower()


def load_cases(path: Path) -> dict[str, dict]:
    cases: dict[str, dict] = {}
    for number, case in _read_jsonl(path, "cases"):
        label = f"cases line {number}"
        case_id = _require_text(case.get("id"), f"{label}.id")
        if not IDENTIFIER.fullmatch(case_id):
            raise InvalidInput(f"{label}.id: expected a short identifier")
        if case_id in cases:
            raise InvalidInput(f"{label}.id: duplicate ID")
        _require_text(case.get("category"), f"{label}.category")
        _require_text(case.get("question"), f"{label}.question")
        if not isinstance(case.get("expected_outcome"), str) or case["expected_outcome"] not in OUTCOMES:
            raise InvalidInput(f"{label}.expected_outcome: unsupported value")
        if not isinstance(case.get("runnable"), bool):
            raise InvalidInput(f"{label}.runnable: expected boolean")

        required = _require_list(case.get("required_sources"), f"{label}.required_sources")
        seen_sources: set[tuple[str, str | None, str | None]] = set()
        for index, item in enumerate(required):
            source = _require_object(item, f"{label}.required_sources[{index}]")
            kind = source.get("kind")
            if not isinstance(kind, str) or kind not in SOURCE_KINDS:
                raise InvalidInput(f"{label}.required_sources[{index}].kind: unsupported value")
            repository = None
            if "repository" in source:
                repository = _repository(source["repository"], f"{label}.required_sources[{index}].repository")
            revision = None
            if "revision" in source:
                revision = _require_text(source["revision"], f"{label}.required_sources[{index}].revision").lower()
                if not SHA40.fullmatch(revision):
                    raise InvalidInput(f"{label}.required_sources[{index}].revision: expected 40-SHA")
            key = (kind, repository, revision)
            if key in seen_sources:
                raise InvalidInput(f"{label}.required_sources[{index}]: duplicate requirement")
            seen_sources.add(key)

        checks = _require_list(case.get("checks"), f"{label}.checks")
        check_ids: set[str] = set()
        for index, item in enumerate(checks):
            check = _require_object(item, f"{label}.checks[{index}]")
            check_id = _require_text(check.get("id"), f"{label}.checks[{index}].id")
            if not IDENTIFIER.fullmatch(check_id):
                raise InvalidInput(f"{label}.checks[{index}].id: expected a short identifier")
            if check_id in check_ids:
                raise InvalidInput(f"{label}.checks[{index}].id: duplicate ID")
            check_ids.add(check_id)
            _require_text(check.get("description"), f"{label}.checks[{index}].description")

        cases[case_id] = case
    if not cases:
        raise InvalidInput("cases: no cases found")
    return cases


def _validate_review(review: object, check_ids: set[str], label: str) -> None:
    if review is None:
        return  # A missing review is pending, never a semantic pass.
    item = _require_object(review, label)
    for field in ("facts", "claim_support"):
        answers = _require_object(item.get(field, {}), f"{label}.{field}")
        for check_id, verdict in answers.items():
            if check_id not in check_ids:
                raise InvalidInput(f"{label}.{field}: unknown check ID")
            if verdict is not None and not isinstance(verdict, bool):
                raise InvalidInput(f"{label}.{field}: expected boolean or null verdict")
    if item.get("outcome") is not None and not isinstance(item["outcome"], bool):
        raise InvalidInput(f"{label}.outcome: expected boolean or null")
    if item.get("scope_safe") is not None and not isinstance(item["scope_safe"], bool):
        raise InvalidInput(f"{label}.scope_safe: expected boolean or null")
    if "notes" in item and not isinstance(item["notes"], str):
        raise InvalidInput(f"{label}.notes: expected string")


def load_observations(path: Path, cases: dict[str, dict]) -> dict[str, dict]:
    observations: dict[str, dict] = {}
    for number, observation in _read_jsonl(path, "observations"):
        label = f"observations line {number}"
        case_id = _require_text(observation.get("case_id"), f"{label}.case_id")
        if case_id not in cases:
            raise InvalidInput(f"{label}.case_id: unknown case")
        if case_id in observations:
            raise InvalidInput(f"{label}.case_id: duplicate ID")
        if not isinstance(observation.get("answer"), str):
            raise InvalidInput(f"{label}.answer: expected string")
        sources = _require_list(observation.get("sources"), f"{label}.sources")
        for index, item in enumerate(sources):
            source = _require_object(item, f"{label}.sources[{index}]")
            if not isinstance(source.get("kind"), str) or source["kind"] not in SOURCE_KINDS:
                raise InvalidInput(f"{label}.sources[{index}].kind: unsupported value")
            _require_text(source.get("url"), f"{label}.sources[{index}].url")
            if "repository" in source:
                _repository(source["repository"], f"{label}.sources[{index}].repository")
            if not isinstance(source.get("provenance_checked"), bool):
                raise InvalidInput(f"{label}.sources[{index}].provenance_checked: expected boolean")
        for field in ("delivery_attempts", "latency_ms", "total_tokens"):
            _require_optional_count(observation.get(field), f"{label}.{field}")
        check_ids = {check["id"] for check in cases[case_id]["checks"]}
        _validate_review(observation.get("review"), check_ids, f"{label}.review")
        observations[case_id] = observation
    return observations


def github_code_location(raw: str) -> tuple[str, str] | None:
    """Return (owner/repo, commit) only for a pinned, short GitHub line link."""
    try:
        parsed = urlsplit(raw)
    except ValueError:
        return None
    if parsed.scheme != "https" or parsed.netloc.lower() != "github.com" or parsed.query:
        return None
    parts = unquote(parsed.path).strip("/").split("/")
    if len(parts) < 5 or parts[2] != "blob" or not SHA40.fullmatch(parts[3]):
        return None
    if not REPOSITORY.fullmatch("/".join(parts[:2])) or any(part in {"", ".", ".."} for part in parts[4:]):
        return None
    match = LINE_FRAGMENT.fullmatch(unquote(parsed.fragment))
    if match is None:
        return None
    try:
        start = int(match.group(1))
        end = int(match.group(2) or match.group(1))
    except ValueError:  # An adversarially long decimal line number.
        return None
    if end < start or end - start + 1 > MAX_CITED_LINES:
        return None
    return f"{parts[0]}/{parts[1]}".lower(), parts[3].lower()


def github_release_repository(raw: str) -> str | None:
    try:
        parsed = urlsplit(raw)
    except ValueError:
        return None
    if parsed.scheme != "https" or parsed.netloc.lower() != "github.com" or parsed.query or parsed.fragment:
        return None
    parts = unquote(parsed.path).strip("/").split("/")
    if len(parts) < 4 or not REPOSITORY.fullmatch("/".join(parts[:2])):
        return None
    if parts[2] != "releases" or not (
        len(parts) == 4 and parts[3] == "latest"
        or len(parts) >= 5 and parts[3] == "tag" and all(part not in {"", ".", ".."} for part in parts[4:])
    ):
        return None
    return f"{parts[0]}/{parts[1]}".lower()


def _trusted_source(source: dict) -> tuple[str, str | None, str | None] | None:
    if not source["provenance_checked"]:
        return None
    kind = source["kind"]
    repository = source.get("repository")
    normalized_repository = repository.lower() if repository else None
    if kind == "github_code":
        location = github_code_location(source["url"])
        if location is None or normalized_repository not in {None, location[0]}:
            return None
        return kind, location[0], location[1]
    if kind == "github_release":
        location = github_release_repository(source["url"])
        if location is None or normalized_repository not in {None, location}:
            return None
        return kind, location, None
    return kind, normalized_repository, None


def _verdict(values: list[bool | None]) -> str:
    if any(value is False for value in values):
        return "fail"
    if any(value is None for value in values):
        return "pending"
    return "pass"


def _review_status(case: dict, observation: dict) -> dict[str, str]:
    review = observation.get("review") or {}
    check_ids = [check["id"] for check in case["checks"]]
    facts = review.get("facts") or {}
    supports = review.get("claim_support") or {}
    result = {
        "outcome": _verdict([review.get("outcome")]),
        "facts": _verdict([facts.get(check_id) for check_id in check_ids]) if check_ids else "not_applicable",
        "claim_support": _verdict([supports.get(check_id) for check_id in check_ids]) if check_ids else "not_applicable",
        "scope_safe": _verdict([review.get("scope_safe")]),
    }
    return result


def _source_status(case: dict, observation: dict) -> tuple[str, int]:
    trusted = [value for source in observation["sources"] if (value := _trusted_source(source))]
    untrusted = len(observation["sources"]) - len(trusted)
    required = case["required_sources"]
    if not required:
        return "not_applicable", untrusted
    for source in required:
        kind = source["kind"]
        repository = source.get("repository", "").lower()
        # Only github_code has a fixed commit in the observation URL. For RAG
        # and Wiki, a required revision is dataset metadata, not proof of the
        # document version; never pretend to have checked it from a bare URL.
        revision = source.get("revision", "").lower() if kind == "github_code" else ""
        if not any(
            found_kind == kind
            and (not repository or found_repository == repository)
            and (not revision or found_revision == revision)
            for found_kind, found_repository, found_revision in trusted
        ):
            return "fail", untrusted
    return "pass", untrusted


def _budget_status(value: int | None, maximum: int | None) -> str:
    if value is None:
        return "pending"
    if maximum is None:
        return "recorded"
    return "pass" if value <= maximum else "fail"


def score_cases(
    cases: dict[str, dict], observations: dict[str, dict],
    max_latency_ms: int | None = None, max_total_tokens: int | None = None,
) -> dict:
    results: list[dict] = []
    statuses = {"pass": 0, "fail": 0, "pending": 0, "skipped": 0}
    latencies: list[int] = []
    tokens: list[int] = []
    for case_id, case in cases.items():
        # Category/question/check descriptions stay in the private fixture.
        result = {"case_id": case_id}
        observation = observations.get(case_id)
        if not case["runnable"]:
            result["status"] = "skipped"
        elif observation is None:
            result["status"] = "pending"
        else:
            source_status, untrusted = _source_status(case, observation)
            delivery = observation.get("delivery_attempts")
            delivery_status = "pending" if delivery is None else "pass" if delivery == 1 else "fail"
            latency = observation.get("latency_ms")
            token_count = observation.get("total_tokens")
            if latency is not None:
                latencies.append(latency)
            if token_count is not None:
                tokens.append(token_count)
            review_status = _review_status(case, observation)
            statuses_to_merge = [
                source_status,
                delivery_status,
                "pass" if observation["answer"].strip() else "fail",
                *review_status.values(),
            ]
            latency_status = _budget_status(latency, max_latency_ms)
            token_status = _budget_status(token_count, max_total_tokens)
            if max_latency_ms is not None:
                statuses_to_merge.append(latency_status)
            if max_total_tokens is not None:
                statuses_to_merge.append(token_status)
            result.update({
                "status": _verdict([False if item == "fail" else None if item == "pending" else True for item in statuses_to_merge]),
                "required_sources": source_status,
                "untrusted_sources": untrusted,
                "delivery": delivery_status,
                "response_present": bool(observation["answer"].strip()),
                "latency_ms": latency,
                "latency": latency_status,
                "total_tokens": token_count,
                "tokens": token_status,
                "review": review_status,
            })
        statuses[result["status"]] += 1
        results.append(result)
    return {
        "ok": True,
        "summary": {
            "cases": len(cases),
            "observations": len(observations),
            "statuses": statuses,
            "latency_ms_median": statistics.median(latencies) if latencies else None,
            "total_tokens_median": statistics.median(tokens) if tokens else None,
        },
        "cases": results,
    }


def _positive_budget(value: str) -> int:
    try:
        result = int(value)
    except ValueError as exc:
        raise argparse.ArgumentTypeError("budget must be a positive integer") from exc
    if result <= 0:
        raise argparse.ArgumentTypeError("budget must be a positive integer")
    return result


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("validate", "score"):
        command = commands.add_parser(name, help=f"{name} offline JSONL inputs")
        command.add_argument("--cases", required=True, type=Path)
        command.add_argument("--observations", required=name == "score", type=Path)
        if name == "score":
            command.add_argument("--max-latency-ms", type=_positive_budget)
            command.add_argument("--max-total-tokens", type=_positive_budget)
    args = parser.parse_args(argv)
    try:
        cases = load_cases(args.cases)
        observations = load_observations(args.observations, cases) if args.observations else {}
    except InvalidInput as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 2
    if args.command == "validate":
        output = {"ok": True, "cases": len(cases), "observations": len(observations)}
    else:
        output = score_cases(cases, observations, args.max_latency_ms, args.max_total_tokens)
    print(json.dumps(output, ensure_ascii=False, indent=2))
    return 0


if __name__ == "__main__":
    sys.exit(main())
