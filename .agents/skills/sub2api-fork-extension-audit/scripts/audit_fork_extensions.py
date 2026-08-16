#!/usr/bin/env python3
"""Read-only audit of Sub2API fork extensions around an upstream merge."""
from __future__ import annotations

import argparse
import fnmatch
import hashlib
import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Any

SHA_RE = re.compile(r"^[0-9a-fA-F]{40}$")
SEVERITY_ORDER = {"pass": 0, "warning": 1, "blocker": 2, "catalog_update_required": 3}


class Audit:
    def __init__(self, root: Path, mode: str, upstream_ref: str, merge_commit: str | None, catalog: Path):
        self.root = root.resolve()
        self.mode = mode
        self.upstream_ref = upstream_ref.lower()
        self.merge_commit = merge_commit.lower() if merge_commit else None
        self.catalog_path = catalog.resolve()
        self.findings: list[dict[str, Any]] = []
        self.catalog: dict[str, Any] = {}
        self.head = ""
        self.merge_base = ""
        self.parents: list[str] = []
        self.diff_paths: list[str] = []
        self.diff_status: dict[str, str] = {}
        self.dirty_paths: list[str] = []

    def git(self, *args: str, check: bool = True) -> str:
        proc = subprocess.run(["git", *args], cwd=self.root, text=True, encoding="utf-8", errors="replace", capture_output=True)
        if check and proc.returncode:
            raise RuntimeError(proc.stderr.strip() or f"git {' '.join(args)} failed")
        if proc.returncode and check:
            raise RuntimeError(proc.stderr.strip())
        return proc.stdout.strip()

    def add(self, level: str, code: str, message: str, **details: Any) -> None:
        self.findings.append({"level": level, "code": code, "message": message, "details": details})

    def check(self) -> None:
        try:
            self.catalog = json.loads(self.catalog_path.read_text(encoding="utf-8"))
        except Exception as exc:
            self.add("blocker", "catalog_unreadable", "无法读取扩展目录", error=str(exc))
            return

        if not SHA_RE.fullmatch(self.upstream_ref):
            self.add("blocker", "short_or_invalid_upstream_sha", "upstream-ref 必须是 40 位完整 SHA", value=self.upstream_ref)
            return
        try:
            self.git("rev-parse", "--verify", f"{self.upstream_ref}^{{commit}}")
        except RuntimeError as exc:
            self.add("blocker", "unknown_upstream_ref", "目标 upstream commit 不存在", error=str(exc))
            return

        try:
            self.head = self.git("rev-parse", "HEAD").lower()
            self.merge_base = self.git("merge-base", self.upstream_ref, self.head).lower()
            status = self.git("status", "--porcelain=v1", check=False)
            self.dirty_paths = [line[3:] for line in status.splitlines() if len(line) >= 3]
            if self.dirty_paths:
                self.add("blocker", "dirty_worktree", "工作区存在未提交改动", paths=sorted(self.dirty_paths))
            conflicts = self.git("diff", "--name-only", "--diff-filter=U", check=False).splitlines()
            if conflicts:
                self.add("blocker", "unresolved_conflicts", "存在未解决冲突", paths=sorted(conflicts))
            status_lines = self.git("diff", "--name-status", f"{self.upstream_ref}...{self.head}").splitlines()
            for line in status_lines:
                fields = line.split("\t")
                if len(fields) >= 2:
                    self.diff_status[fields[-1]] = fields[0]
            self.diff_paths = sorted(self.diff_status)
            self.parents = self.git("rev-list", "--parents", "-n", "1", self.head).split()[1:]
        except RuntimeError as exc:
            self.add("blocker", "git_identity_unreadable", "无法读取 Git 合并身份", error=str(exc))
            return

        self.add("pass", "git_identity", "Git 身份和 merge-base 已读取", head=self.head, upstream=self.upstream_ref, merge_base=self.merge_base, parents=self.parents)
        if self.mode == "post-merge":
            self.check_post_merge_identity()
        self.check_versions()
        self.check_catalog_markers()
        self.check_extensions()
        self.check_migrations()
        self.check_profiles()
        self.check_unregistered_paths()
        if self.mode == "post-merge":
            self.check_whole_file_resolution()
        self.check_generated_drift()

    def check_post_merge_identity(self) -> None:
        if not self.merge_commit or not SHA_RE.fullmatch(self.merge_commit):
            self.add("blocker", "short_or_invalid_merge_sha", "post-merge 必须传入 40 位完整 merge commit", value=self.merge_commit)
            return
        try:
            actual = self.git("rev-parse", "--verify", f"{self.merge_commit}^{{commit}}").lower()
            parents = self.git("rev-list", "--parents", "-n", "1", actual).split()[1:]
            expected_head = actual
        except RuntimeError as exc:
            self.add("blocker", "unknown_merge_commit", "merge commit 不存在", error=str(exc))
            return
        if len(parents) < 2 or parents[1].lower() != self.upstream_ref:
            self.add("blocker", "wrong_merge_parent", "merge commit 第二父提交不是目标 upstream SHA", merge_commit=expected_head, parents=parents, expected_upstream=self.upstream_ref)
        else:
            self.add("pass", "merge_parent", "merge commit 第二父提交严格匹配 upstream", merge_commit=expected_head, parents=parents)
        self.head = actual
        self.parents = parents

    def show(self, ref: str, path: str) -> str | None:
        proc = subprocess.run(["git", "show", f"{ref}:{path}"], cwd=self.root, text=True, encoding="utf-8", errors="replace", capture_output=True)
        return proc.stdout if proc.returncode == 0 else None

    def check_versions(self) -> None:
        upstream = self.show(self.upstream_ref, "backend/cmd/server/VERSION")
        current = self.show(self.head, "backend/cmd/server/VERSION") or ""
        if upstream is None:
            self.add("blocker", "upstream_version_missing", "官方目标缺少 VERSION 文件")
            return
        official = upstream.strip()
        fork = current.strip()
        expected = official + self.catalog.get("version_contract", {}).get("fork_suffix", "-baiyu")
        if fork != expected:
            self.add("blocker", "fork_version_mismatch", "fork VERSION 未按官方版本追加 -baiyu", official=official, fork=fork, expected=expected)
        else:
            self.add("pass", "version_contract", "VERSION 满足官方版本加后缀合同", official=official, fork=fork)

    def path_exists(self, pattern: str) -> list[str]:
        matches = []
        prefix = pattern[:-3].rstrip("/") if pattern.endswith("/**") else ""
        candidates = (self.root / prefix).rglob("*") if prefix else self.root.glob(pattern)
        for p in candidates:
            if p.is_file():
                relative = p.relative_to(self.root).as_posix()
                if not prefix or relative.startswith(prefix + "/"):
                    matches.append(relative)
        return sorted(matches)

    def grep(self, needle: str) -> list[str]:
        proc = subprocess.run(["git", "grep", "-n", "-F", "-e", needle, "--"], cwd=self.root, text=True, encoding="utf-8", errors="replace", capture_output=True)
        if proc.returncode not in (0, 1):
            return []
        try:
            catalog_rel = self.catalog_path.relative_to(self.root).as_posix()
        except ValueError:
            catalog_rel = ""
        evidence = []
        for line in (proc.stdout or "").splitlines():
            path = line.split(":", 1)[0].replace("\\", "/")
            if path == catalog_rel or path.startswith(".agents/skills/sub2api-fork-extension-audit/"):
                continue
            evidence.append(line)
            if len(evidence) == 5:
                break
        return evidence

    def check_catalog_markers(self) -> None:
        for path, markers in self.catalog.get("migration_assertions", {}).items():
            full = self.root / path
            if not full.is_file():
                self.add("blocker", "migration_assertion_file_missing", "迁移断言文件不存在", path=path)
                continue
            text = full.read_text(encoding="utf-8", errors="replace")
            missing = [marker for marker in markers if marker not in text]
            if missing:
                self.add("blocker", "migration_semantic_marker_missing", "关键迁移语义标记缺失", path=path, missing=missing)
            else:
                self.add("pass", "migration_semantic_markers", "关键迁移语义标记存在", path=path)

    def check_extensions(self) -> None:
        for ext in self.catalog.get("extensions", []):
            eid = ext["id"]
            missing_paths = [pattern for pattern in ext.get("paths", []) if not self.path_exists(pattern)]
            if missing_paths:
                self.add("blocker", "extension_path_missing", f"扩展 {eid} 的登记路径不存在", extension_id=eid, paths=missing_paths)
            for field in ("symbols", "api_routes", "settings_keys"):
                missing = [value for value in ext.get(field, []) if not self.grep(value)]
                if missing:
                    self.add("blocker", "extension_marker_missing", f"扩展 {eid} 的 {field} 标记不存在", extension_id=eid, field=field, values=missing)
            missing_tests = [pattern for pattern in ext.get("required_tests", []) if not self.path_exists(pattern)]
            if missing_tests:
                self.add("blocker", "required_test_missing", f"扩展 {eid} 的最低测试不存在", extension_id=eid, tests=missing_tests)
            else:
                self.add("pass", "extension_catalog_entry", f"扩展 {eid} 的路径、标记和最低测试已发现", extension_id=eid)

    def all_profile_migrations(self) -> set[str]:
        result: set[str] = set()
        source = self.root / self.catalog.get("profile_source", "")
        if not source.is_file():
            return result
        try:
            namespace: dict[str, Any] = {}
            exec(compile(source.read_text(encoding="utf-8"), str(source), "exec"), namespace)
            for profile in namespace.get("PROFILES", {}).values():
                result.update(profile.get("migrations", []))
        except Exception as exc:
            self.add("blocker", "profile_source_unreadable", "无法读取 profile manifest 源", error=str(exc))
        return result

    def check_migrations(self) -> None:
        contracts = self.catalog.get("migration_contracts", {})
        for filename, expected in sorted(contracts.items()):
            path = self.root / "backend/migrations" / filename
            if not path.is_file():
                self.add("blocker", "migration_missing", "登记迁移不存在", migration=filename)
                continue
            actual = hashlib.sha256(path.read_bytes()).hexdigest()
            if actual != expected:
                self.add("blocker", "migration_checksum_drift", "迁移 checksum 漂移", migration=filename, expected=expected, actual=actual)
        if contracts:
            self.add("pass", "migration_checksums", "登记迁移 checksum 已核验", count=len(contracts))

    def check_profiles(self) -> None:
        source = self.root / self.catalog.get("profile_source", "")
        if not source.is_file():
            self.add("blocker", "profile_source_missing", "profile manifest 源不存在", path=str(source.relative_to(self.root)))
            return
        try:
            namespace: dict[str, Any] = {}
            exec(compile(source.read_text(encoding="utf-8"), str(source), "exec"), namespace)
            profiles = namespace.get("PROFILES", {})
        except Exception as exc:
            self.add("blocker", "profile_source_unreadable", "profile manifest 无法执行读取", error=str(exc))
            return
        for name, expected in sorted(self.catalog.get("historical_profiles", {}).items()):
            profile = profiles.get(name)
            if not profile:
                self.add("blocker", "historical_profile_missing", "历史 profile 不存在", profile=name)
                continue
            migrations = profile.get("migrations", [])
            migration_map: dict[str, str] = {}
            missing = []
            for migration in migrations:
                path = self.root / "backend/migrations" / migration
                if not path.is_file():
                    missing.append(migration)
                else:
                    migration_map[migration] = hashlib.sha256(path.read_bytes()).hexdigest()
            digest = hashlib.sha256(json.dumps(migration_map, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            fields = {key: profile.get(key) for key in ("version", "compatibility_version", "compatibility_commit", "compatibility_image_id")}
            mismatches = {}
            for key, value in expected.items():
                actual = len(migrations) if key == "migration_count" else digest if key == "migration_map_sha256" else fields.get(key)
                if actual != value:
                    mismatches[key] = {"expected": value, "actual": actual}
            if missing:
                mismatches["missing_migrations"] = missing
            if mismatches:
                self.add("blocker", "historical_profile_drift", "历史 profile、migration map 或 compatibility identity 漂移", profile=name, mismatches=mismatches)
            else:
                self.add("pass", "historical_profile_contract", "历史 profile 合同保持不变", profile=name)

    def check_unregistered_paths(self) -> None:
        support = self.catalog.get("registered_support_paths", [])
        extensions = [pattern for ext in self.catalog.get("extensions", []) for pattern in ext.get("paths", [])]
        all_patterns = support + extensions
        unknown = [path for path in self.diff_paths if not any(fnmatch.fnmatch(path, pattern) for pattern in all_patterns)]
        migrations = self.all_profile_migrations() | set(self.catalog.get("migration_contracts", {}))
        new_migrations = [path for path in self.diff_paths if self.diff_status.get(path, "")[0] != "D" and path.startswith("backend/migrations/") and path.endswith(".sql") and Path(path).name not in migrations]
        if unknown:
            self.add("catalog_update_required", "unregistered_fork_paths", "发现未登记的 fork-only 路径", paths=unknown)
        else:
            self.add("pass", "catalog_paths", "fork-only 路径均落在已登记范围", count=len(self.diff_paths))
        if new_migrations:
            self.add("catalog_update_required", "unregistered_migrations", "发现未登记的新增 migration", migrations=new_migrations)

    def check_whole_file_resolution(self) -> None:
        if not self.merge_commit or len(self.parents) < 2:
            return
        base = self.git("merge-base", self.parents[0], self.parents[1])
        left = set(self.git("diff", "--name-only", f"{base}..{self.parents[0]}").splitlines())
        right = set(self.git("diff", "--name-only", f"{base}..{self.parents[1]}").splitlines())
        risky = [path for path in sorted(left & right) if any(fnmatch.fnmatch(path, pattern) for pattern in self.catalog.get("high_risk_paths", []))]
        suspected = []
        for path in risky:
            result = self.show(self.head, path)
            first = self.show(self.parents[0], path)
            second = self.show(self.parents[1], path)
            if result is not None and first != second and result in (first, second):
                suspected.append(path)
        if suspected:
            self.add("warning", "whole_file_resolution_suspected", "高风险冲突结果与单一父提交完全相同，需要人工语义复核", paths=suspected)
        else:
            self.add("pass", "high_risk_resolution", "未发现疑似整文件 ours/theirs 结果")

    def check_generated_drift(self) -> None:
        generated = [path for path in self.dirty_paths if any(token in path for token in ("/ent/", "wire_gen.go", "/wire.go"))]
        if generated:
            self.add("blocker", "generated_file_drift", "生成代码存在未提交漂移", paths=sorted(generated))

    def report(self) -> dict[str, Any]:
        levels = sorted({item["level"] for item in self.findings}, key=lambda value: SEVERITY_ORDER[value])
        status = "blocker" if "blocker" in levels else "catalog_update_required" if "catalog_update_required" in levels else "warning" if "warning" in levels else "pass"
        return {"schema": 1, "mode": self.mode, "status": status, "head": self.head, "upstream_ref": self.upstream_ref, "merge_base": self.merge_base, "merge_commit": self.merge_commit, "parents": self.parents, "diff_paths": self.diff_paths, "fork_only_commits": self.git("rev-list", f"{self.upstream_ref}..{self.head}").splitlines() if self.head else [], "findings": sorted(self.findings, key=lambda item: (SEVERITY_ORDER[item["level"]], item["code"], json.dumps(item.get("details", {}), sort_keys=True))) }

    def write(self) -> tuple[Path, dict[str, Any]]:
        report = self.report()
        audit_id = f"{self.mode}-{(self.head or 'unknown')[:12]}-{self.upstream_ref[:12]}"
        out = self.root / ".tmp" / "fork-extension-audit" / audit_id
        out.mkdir(parents=True, exist_ok=True)
        (out / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        lines = [f"# Fork Extension Audit: {audit_id}", "", f"- status: `{report['status']}`", f"- mode: `{self.mode}`", f"- head: `{self.head}`", f"- upstream: `{self.upstream_ref}`", f"- merge-base: `{self.merge_base}`", "", "## Findings", ""]
        for item in report["findings"]:
            details = json.dumps(item.get("details", {}), ensure_ascii=False, sort_keys=True)
            lines.append(f"- `{item['level']}` `{item['code']}` — {item['message']}  ")
            lines.append(f"  `{details}`")
        (out / "report.md").write_text("\n".join(lines) + "\n", encoding="utf-8")
        return out, report


def main() -> int:
    parser = argparse.ArgumentParser(description="Audit Sub2API fork extensions without changing the repository")
    parser.add_argument("mode", choices=("snapshot", "pre-merge", "post-merge"))
    parser.add_argument("--upstream-ref", required=True)
    parser.add_argument("--merge-commit")
    parser.add_argument("--repo-root", default=".", help=argparse.SUPPRESS)
    parser.add_argument("--catalog", help=argparse.SUPPRESS)
    args = parser.parse_args()
    root = Path(args.repo_root).resolve()
    catalog = Path(args.catalog).resolve() if args.catalog else root / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml"
    audit = Audit(root, args.mode, args.upstream_ref, args.merge_commit, catalog)
    audit.check()
    out, report = audit.write()
    print(json.dumps({"status": report["status"], "audit_dir": out.relative_to(root).as_posix(), "finding_count": len(report["findings"])}, ensure_ascii=False, sort_keys=True))
    return 1 if report["status"] in ("blocker", "catalog_update_required") else 0


if __name__ == "__main__":
    raise SystemExit(main())
