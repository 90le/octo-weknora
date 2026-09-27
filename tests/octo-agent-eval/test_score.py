"""No network, model, database, or private fixture is used by these tests."""

from __future__ import annotations

import contextlib
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from score import (  # noqa: E402
    InvalidInput,
    github_code_location,
    load_cases,
    load_observations,
    main,
    score_cases,
)


REVISION = "a" * 40
OTHER_REVISION = "b" * 40
SOURCE_URL = f"https://github.com/example/project/blob/{REVISION}/src/answer.go#L10-L12"


def case(**updates):
    value = {
        "id": "public-source-01",
        "category": "code",
        "question": "PRIVATE_QUESTION_SENTINEL: Which function is called?",
        "expected_outcome": "answer",
        "required_sources": [
            {"kind": "github_code", "repository": "example/project", "revision": REVISION}
        ],
        "checks": [{"id": "function", "description": "PRIVATE_CHECK_DESCRIPTION"}],
        "runnable": True,
    }
    value.update(updates)
    return value


def observation(**updates):
    value = {
        "case_id": "public-source-01",
        "answer": "PRIVATE_ANSWER_SENTINEL",
        "sources": [
            {
                "kind": "github_code",
                "url": SOURCE_URL,
                "repository": "example/project",
                "provenance_checked": True,
            }
        ],
        "delivery_attempts": 1,
        "latency_ms": 3200,
        "total_tokens": 8400,
        "review": {
            "outcome": True,
            "facts": {"function": True},
            "claim_support": {"function": True},
            "scope_safe": True,
            "notes": "PRIVATE_REVIEW_SENTINEL",
        },
    }
    value.update(updates)
    return value


def write_jsonl(path: Path, rows: list[dict]) -> None:
    path.write_text("".join(json.dumps(row) + "\n" for row in rows), encoding="utf-8")


class SourceURLTests(unittest.TestCase):
    def test_pinned_short_code_link_is_valid(self):
        self.assertEqual(github_code_location(SOURCE_URL), ("example/project", REVISION))
        self.assertEqual(
            github_code_location(SOURCE_URL.replace("#L10-L12", "#L10")),
            ("example/project", REVISION),
        )

    def test_forged_branch_wrong_host_and_broad_range_are_not_valid(self):
        for raw in (
            SOURCE_URL.replace(REVISION, "main"),
            SOURCE_URL.replace("github.com", "github.com.evil.example"),
            SOURCE_URL.replace("#L10-L12", "#L10-L22"),
            SOURCE_URL.replace("#L10-L12", "#L0"),
            SOURCE_URL.replace("https://", "http://"),
        ):
            with self.subTest(raw=raw):
                self.assertIsNone(github_code_location(raw))


class JSONLAndScoreTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.cases_file = self.directory / "cases.jsonl"
        self.observations_file = self.directory / "observations.jsonl"

    def run_cli(self, *arguments):
        stdout = io.StringIO()
        stderr = io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            code = main(list(arguments))
        return code, stdout.getvalue(), stderr.getvalue()

    def test_normal_score_passes_without_emitting_private_content(self):
        write_jsonl(self.cases_file, [case()])
        write_jsonl(self.observations_file, [observation()])
        code, output, error = self.run_cli(
            "score", "--cases", str(self.cases_file),
            "--observations", str(self.observations_file),
            "--max-latency-ms", "4000", "--max-total-tokens", "9000",
        )
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(json.loads(output)["cases"][0]["status"], "pass")
        for private in (
            "PRIVATE_QUESTION_SENTINEL", "PRIVATE_ANSWER_SENTINEL",
            "PRIVATE_CHECK_DESCRIPTION", "PRIVATE_REVIEW_SENTINEL", SOURCE_URL,
        ):
            self.assertNotIn(private, output)

    def test_forged_code_link_and_wrong_revision_are_quality_failures_not_process_errors(self):
        write_jsonl(self.cases_file, [case()])
        for invalid_url in (SOURCE_URL.replace(REVISION, "main"), SOURCE_URL.replace(REVISION, OTHER_REVISION)):
            with self.subTest(invalid_url=invalid_url):
                write_jsonl(self.observations_file, [
                    observation(sources=[{
                        "kind": "github_code", "url": invalid_url,
                        "repository": "example/project", "provenance_checked": True,
                    }])
                ])
                code, output, error = self.run_cli(
                    "score", "--cases", str(self.cases_file),
                    "--observations", str(self.observations_file),
                )
                self.assertEqual((code, error), (0, ""))
                result = json.loads(output)["cases"][0]
                self.assertEqual((result["status"], result["required_sources"]), ("fail", "fail"))
                self.assertNotIn(invalid_url, output)

    def test_missing_review_is_pending_not_automatically_passed(self):
        write_jsonl(self.cases_file, [case()])
        write_jsonl(self.observations_file, [observation(review=None)])
        code, output, error = self.run_cli(
            "score", "--cases", str(self.cases_file),
            "--observations", str(self.observations_file),
        )
        self.assertEqual((code, error), (0, ""))
        result = json.loads(output)["cases"][0]
        self.assertEqual(result["status"], "pending")
        self.assertEqual(result["review"], {
            "outcome": "pending", "facts": "pending", "claim_support": "pending", "scope_safe": "pending",
        })

    def test_outcome_review_is_explicit_and_never_guessed_from_answer(self):
        cases = {"public-source-01": case(expected_outcome="abstain")}
        pending = score_cases(cases, {"public-source-01": observation(review={
            "facts": {"function": True}, "claim_support": {"function": True}, "scope_safe": True,
        })})["cases"][0]
        self.assertEqual((pending["review"]["outcome"], pending["status"]), ("pending", "pending"))
        failed = score_cases(cases, {"public-source-01": observation(review={
            "outcome": False, "facts": {"function": True},
            "claim_support": {"function": True}, "scope_safe": True,
        })})["cases"][0]
        self.assertEqual((failed["review"]["outcome"], failed["status"]), ("fail", "fail"))

    def test_false_semantic_review_fails_but_null_stays_pending(self):
        cases = {"public-source-01": case()}
        pending = score_cases(cases, {"public-source-01": observation(
            review={"facts": {"function": True}, "claim_support": {"function": None}, "scope_safe": True}
        )})
        self.assertEqual(pending["cases"][0]["status"], "pending")
        failed = score_cases(cases, {"public-source-01": observation(
            review={"facts": {"function": True}, "claim_support": {"function": False}, "scope_safe": True}
        )})
        self.assertEqual(failed["cases"][0]["status"], "fail")

    def test_delivery_and_optional_budgets_are_independent(self):
        cases = {"public-source-01": case()}
        delayed = observation(delivery_attempts=2, latency_ms=5000, total_tokens=10000)
        result = score_cases(cases, {"public-source-01": delayed}, 4000, 9000)["cases"][0]
        self.assertEqual(result["delivery"], "fail")
        self.assertEqual(result["latency"], "fail")
        self.assertEqual(result["tokens"], "fail")
        unbounded = score_cases(cases, {"public-source-01": observation(latency_ms=None, total_tokens=None)})["cases"][0]
        self.assertEqual(unbounded["latency"], "pending")
        self.assertEqual(unbounded["tokens"], "pending")
        self.assertEqual(unbounded["status"], "pass", "missing metrics do not fail unless a budget was requested")

    def test_duplicate_case_and_observation_ids_are_invalid_input(self):
        write_jsonl(self.cases_file, [case(), case()])
        code, output, error = self.run_cli("validate", "--cases", str(self.cases_file))
        self.assertEqual(code, 2)
        self.assertEqual(output, "")
        self.assertIn("duplicate ID", error)
        self.assertNotIn("PRIVATE_QUESTION_SENTINEL", error)

        write_jsonl(self.cases_file, [case()])
        write_jsonl(self.observations_file, [observation(), observation()])
        with self.assertRaisesRegex(InvalidInput, "duplicate ID"):
            load_observations(self.observations_file, load_cases(self.cases_file))

    def test_unknown_review_check_is_invalid_but_missing_observation_is_pending(self):
        write_jsonl(self.cases_file, [case()])
        write_jsonl(self.observations_file, [observation(review={"facts": {"typo": True}})])
        code, _, error = self.run_cli(
            "validate", "--cases", str(self.cases_file),
            "--observations", str(self.observations_file),
        )
        self.assertEqual(code, 2)
        self.assertIn("unknown check ID", error)

        self.observations_file.write_text("", encoding="utf-8")
        code, output, error = self.run_cli(
            "score", "--cases", str(self.cases_file),
            "--observations", str(self.observations_file),
        )
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(json.loads(output)["cases"][0]["status"], "pending")

    def test_revision_metadata_is_allowed_for_docs_but_must_be_sha40(self):
        write_jsonl(self.cases_file, [case(required_sources=[
            {"kind": "github_release", "repository": "example/project", "revision": REVISION}
        ])])
        self.assertEqual(len(load_cases(self.cases_file)), 1)
        write_jsonl(self.cases_file, [case(required_sources=[
            {"kind": "kb_document", "repository": "example/project", "revision": "main"}
        ])])
        with self.assertRaisesRegex(InvalidInput, "revision"):
            load_cases(self.cases_file)

    def test_non_runnable_case_is_skipped_and_validate_does_not_need_observations(self):
        write_jsonl(self.cases_file, [case(runnable=False)])
        code, output, error = self.run_cli("validate", "--cases", str(self.cases_file))
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(json.loads(output), {"ok": True, "cases": 1, "observations": 0})
        result = score_cases(load_cases(self.cases_file), {})["cases"][0]
        self.assertEqual(result["status"], "skipped")


if __name__ == "__main__":
    unittest.main()
