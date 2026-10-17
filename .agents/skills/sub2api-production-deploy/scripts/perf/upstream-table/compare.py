"""Compare paired runs without mixing binaries or inventing small-sample P99."""
from __future__ import annotations

import argparse
import json
import math
import pathlib
import statistics
from collections import defaultdict

ROOT = pathlib.Path(__file__).resolve().parents[6]
ARTIFACTS = ROOT / ".tmp/upstream-table-perf"


def distribution(values):
    values = sorted(values)
    result = {"samples": len(values), "p99_eligible": len(values) >= 1000}
    for percentile in (50, 95, 99):
        if values and (percentile != 99 or len(values) >= 1000):
            result[f"p{percentile}_ms"] = values[(len(values) - 1) * percentile // 100]
    return result


def read_samples(entry):
    path = ARTIFACTS / entry["artifacts_directory"] / "latency_samples.json"
    try:
        data = json.loads(path.read_text())
    except (OSError, ValueError):
        return {}, ["missing_or_unreadable_sample_file"]
    if not isinstance(data, dict):
        return {}, ["invalid_sample_document"]
    issues = []
    for route, field in (("admin_ms", "admin"), ("list_ms", "list_http"), ("gateway_ms", "gateway")):
        values = data.get(route)
        count = entry["result"].get(field, {}).get("samples")
        if not isinstance(values, list):
            issues.append(route + "_missing_or_invalid")
            continue
        if type(count) is not int or count <= 0 or len(values) != count:
            issues.append(route + "_sample_count_mismatch")
        try:
            valid = all(type(value) in (int, float) and value >= 0 and math.isfinite(value) for value in values)
        except OverflowError:
            valid = False
        if not valid:
            issues.append(route + "_invalid_sample_value")
    return ({} if issues else data), issues


def summarize(entries):
    raw = defaultdict(list)
    sample_issues = []
    for entry in entries:
        data, issues = read_samples(entry)
        if issues:
            sample_issues.append({"cell": entry["cell"], "issues": issues})
        else:
            for name in ("admin_ms", "list_ms", "gateway_ms"):
                raw[name].extend(data[name])
    summary = {
        "repetitions": len(entries),
        "binary_sha256": sorted({entry["binary_sha256"] for entry in entries}),
        "http_errors": sum(entry["result"]["admin_errors"] for entry in entries),
        "gateway_errors": sum(entry["result"]["gateway_errors"] for entry in entries),
        "failed_runs": sum(entry["exit"] != 0 for entry in entries),
        "latency": {name: distribution(values) for name, values in raw.items()},
        "raw_sample_issues": sample_issues,
        "source_manifests": sorted({entry.get("_source_manifest", "") for entry in entries}),
        "initial_protection_ready": all(entry["result"].get("initial_cache_active") is True and entry["result"].get("initial_all_accounts_fresh") is True for entry in entries),
    }
    for metric in ("sql_driver_calls", "sql_returned_rows", "allocated_bytes", "allocations",
                   "admin_sql_driver_calls", "admin_sql_returned_rows", "gateway_sql_driver_calls",
                   "gateway_sql_returned_rows", "pending_completion", "max_observed_account1_refresh_lag_ms",
                   "measurement_process_cpu_seconds", "pg_temp_files", "pg_temp_bytes", "pg_deadlocks",
                   "pg_blocks_read", "pg_lock_wait_connection_samples", "pg_max_waiting_connections", "pg_wait_read_errors"):
        values = [entry["result"][metric] for entry in entries if metric in entry["result"]]
        if values:
            summary[metric + "_median"] = statistics.median(values)
    summary["protection_and_cleanup_observations"] = [{"cell": entry["cell"], **{
        metric: entry["result"].get(metric) for metric in (
            "refresh_health_samples", "refresh_unavailable_samples", "refresh_stale_samples",
            "pending_completion", "oldest_pending_age_ms", "cleanup_seeded", "cleanup_remaining",
            "budget_fallback_logs", "final_snapshot_available", "final_snapshot_fresh",
            "gateway_statuses", "refresh_population_timeline")}} for entry in entries]
    cpu_samples = [entry.get("postgres_container_cpu", {}) for entry in entries]
    summary["postgres_container_cpu_status"] = [sample.get("status", "unavailable") for sample in cpu_samples]
    if all(sample.get("status") == "ok" for sample in cpu_samples):
        summary["postgres_container_cpu_seconds_median"] = statistics.median(sample["cpu_seconds"] for sample in cpu_samples)
    for metric in ("admin_sql_driver_calls", "admin_sql_returned_rows"):
        calls = sum(entry["result"].get(metric, 0) for entry in entries)
        samples = sum(entry["result"]["admin"]["samples"] for entry in entries)
        if samples and all(metric in entry["result"] for entry in entries):
            summary[metric + "_per_refresh"] = calls / samples
    return summary


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("inputs", nargs="+", type=pathlib.Path)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    groups = defaultdict(lambda: defaultdict(list))
    rejected = []
    seen = set()
    for source in args.inputs:
        provenance_file = source.with_name(source.stem + "-provenance.json")
        provenance = json.loads(provenance_file.read_text()) if provenance_file.is_file() else {}
        for entry in json.loads(source.read_text()):
            identity = (str((ARTIFACTS / entry["artifacts_directory"]).resolve()), entry["binary_sha256"])
            if identity in seen:
                raise ValueError(f"duplicate run: {entry['cell']}")
            seen.add(identity)
            if not entry.get("result"):
                rejected.append({"cell": entry["cell"], "reason": "missing_result"})
                continue
            data = entry["result"]
            version = entry.get("version", entry["cell"].split("-", 1)[0])
            entry["_provenance_valid"] = bool(provenance.get("fixture_sha256")) and all((
                isinstance(provenance.get("measurement_seconds"), (int, float)) and provenance["measurement_seconds"] > 0,
                isinstance(provenance.get("readiness_seconds"), (int, float)) and provenance["readiness_seconds"] > 0,
                all(provenance.get("tool_sha256", {}).get(name) for name in ("prepare.py", "run.py", "pg_cpu_sample.py", "remote_job.py")),
                bool(provenance.get("source_manifest_sha256", {}).get(version)),
                provenance.get("binaries", {}).get(version) == entry["binary_sha256"],
                provenance.get("admins") == data["admin_workers"],
                provenance.get("quality_filter", "") == entry.get("quality_filter", ""),
                provenance.get("page_number", 1) == entry.get("page_number", 1),
            ))
            entry["_measurement_identity"] = json.dumps({name: provenance.get(name) for name in (
                "fixture_sha256", "measurement_seconds", "readiness_seconds")}, sort_keys=True) + json.dumps({
                    name: provenance.get("tool_sha256", {}).get(name) for name in (
                        "prepare.py", "run.py", "pg_cpu_sample.py", "remote_job.py")}, sort_keys=True)
            entry["_source_manifest"] = provenance.get("source_manifest_sha256", {}).get(version, "")
            key = (data["accounts"], data["history"], data["page"], data["admin_workers"],
                   data["scenario"], entry.get("quality_filter", ""), entry.get("page_number", 1))
            groups[key][version].append(entry)
    comparisons = []
    for key, versions in sorted(groups.items()):
        summaries = {version: summarize(entries) for version, entries in versions.items()}
        item = {"accounts": key[0], "history": key[1], "page_size": key[2], "admins": key[3],
                "scenario": key[4], "quality_filter": key[5], "page_number": key[6], "versions": summaries}
        reasons = []
        all_entries = [entry for entries in versions.values() for entry in entries]
        if not all(entry["_provenance_valid"] for entry in all_entries):
            reasons.append("missing_or_inconsistent_provenance")
        if len({entry["_measurement_identity"] for entry in all_entries}) != 1:
            reasons.append("mixed_measurement_conditions")
        if set(summaries) != {"baseline", "optimized"}:
            reasons.append("missing_version")
        if any(summary["repetitions"] < 3 or len(summary["binary_sha256"]) != 1 for summary in summaries.values()):
            reasons.append("three_runs_of_one_binary_required")
        if any(len(summary["source_manifests"]) != 1 or not summary["source_manifests"][0] for summary in summaries.values()):
            reasons.append("mixed_or_missing_source_manifests")
        if not all(summary["initial_protection_ready"] for summary in summaries.values()):
            reasons.append("protection_not_ready_at_measurement_start")
        if any(summary["http_errors"] or summary["failed_runs"] for summary in summaries.values()):
            reasons.append("http_or_run_failure")
        if any(summary["raw_sample_issues"] for summary in summaries.values()):
            reasons.append("invalid_or_missing_raw_samples")
        if key[4] != "full" and any(summary["gateway_errors"] for summary in summaries.values()):
            reasons.append("gateway_failure")
        if key[4] == "full":
            for entry in all_entries:
                data = entry["result"]
                samples = data.get("gateway", {}).get("samples", 0)
                if not (samples > 0 and data.get("full_expected_rejections") == samples
                        and data.get("full_unexpected_responses") == 0 and data.get("full_invalid_json") == 0
                        and data.get("upstream_requests") == 0):
                    reasons.append("full_capacity_rejection_semantics_require_verification")
        changes = {}
        if "baseline" in summaries and "optimized" in summaries:
            for route in ("admin_ms", "list_ms", "gateway_ms"):
                before = summaries["baseline"]["latency"].get(route, {})
                after = summaries["optimized"]["latency"].get(route, {})
                for percentile in (95, 99):
                    metric = f"p{percentile}_ms"
                    if metric not in before or metric not in after:
                        reasons.append(f"{route}_{metric}_insufficient_samples")
                        continue
                    ratio = after[metric] / before[metric] if before[metric] else None
                    changes[f"{route}_{metric}_ratio"] = ratio
                    heavy = key[0] >= 1000 and key[2] == 100 and key[3] == 20
                    maximum = .75 if heavy and route == "admin_ms" and percentile == 95 else 1.05
                    if ratio is None or ratio > maximum:
                        reasons.append(f"{route}_{metric}_target_missed")
        item.update(ratios=changes, issues=sorted(set(reasons)), status="pass" if not reasons else "not_passed")
        comparisons.append(item)
    # This is a measured-cell report, never a claim that the entire approved
    # matrix, browser acceptance, or release Gate has been completed.
    report = {"scope": "latency subreport for supplied cells only; protection, cleanup, browser and release acceptance require separate review", "full_acceptance_complete": False, "rejected_runs": rejected, "comparisons": comparisons,
              "all_supplied_cells_passed": bool(comparisons) and not rejected and all(item["status"] == "pass" for item in comparisons)}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"compared_cells": len(comparisons), "all_supplied_cells_passed": report["all_supplied_cells_passed"], "output": str(args.output)}))


if __name__ == "__main__":
    main()
