#!/usr/bin/env python3
"""Select and record rotating live-model evaluation targets."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import sys
import tempfile
from typing import Any

import yaml


REPO_ROOT = pathlib.Path(__file__).resolve().parents[3]
DEFAULT_MODEL_LIST = REPO_ROOT / "tests" / "eval" / "live-models.yaml"
DEFAULT_STATE_PATH = REPO_ROOT / ".juex" / "live-model-rotation.json"


def main() -> int:
    return main_with_args(sys.argv[1:])


def main_with_args(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="Rotate live provider/evaluation model refs.")
    parser.add_argument(
        "--model-list",
        default=os.environ.get("JUEX_LIVE_MODEL_LIST") or str(DEFAULT_MODEL_LIST),
        help="YAML file that contains provider_smoke_models and compaction_eval_models.",
    )
    parser.add_argument(
        "--state",
        default=os.environ.get("JUEX_LIVE_MODEL_ROTATION_STATE") or str(DEFAULT_STATE_PATH),
        help="Local JSON state file. Defaults to .juex/live-model-rotation.json.",
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    select_parser = subparsers.add_parser("select", help="Print the next model ref without updating state.")
    add_section_arg(select_parser)
    select_parser.add_argument("--format", choices=("plain", "json"), default="plain")

    mark_parser = subparsers.add_parser("mark-success", help="Record a successfully completed model ref.")
    add_section_arg(mark_parser)
    mark_parser.add_argument("--model", required=True)

    status_parser = subparsers.add_parser("status", help="Print the current rotation state.")
    add_section_arg(status_parser)

    parsed = parser.parse_args(argv)
    model_list_path = pathlib.Path(parsed.model_list).expanduser()
    state_path = pathlib.Path(parsed.state).expanduser()

    try:
        refs = load_model_refs(model_list_path, parsed.section)
        state = load_state(state_path)
        if parsed.command == "select":
            selected = select_next(refs, state, parsed.section)
            if parsed.format == "json":
                print(json.dumps({"section": parsed.section, "model": selected}, ensure_ascii=False, separators=(",", ":")))
            else:
                print(selected)
            return 0
        if parsed.command == "mark-success":
            if parsed.model not in refs:
                raise ValueError(f"model {parsed.model!r} is not listed in {model_list_path} section {parsed.section}")
            mark_success(state, parsed.section, parsed.model)
            write_state(state_path, state)
            return 0
        if parsed.command == "status":
            section_state = section_record(state, parsed.section)
            selected = select_next(refs, state, parsed.section)
            print(
                json.dumps(
                    {
                        "section": parsed.section,
                        "last_successful": section_state.get("last_successful", ""),
                        "next": selected,
                        "state": str(state_path),
                        "model_list": str(model_list_path),
                    },
                    ensure_ascii=False,
                    indent=2,
                )
            )
            return 0
    except (OSError, ValueError, yaml.YAMLError) as exc:
        print(str(exc), file=sys.stderr)
        return 1
    return 2


def add_section_arg(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--section", required=True, choices=("provider_smoke_models", "compaction_eval_models"))


def load_model_refs(path: pathlib.Path, section: str) -> list[str]:
    data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    if not isinstance(data, dict):
        raise ValueError(f"{path} must contain a YAML mapping")
    values = data.get(section)
    if not isinstance(values, list):
        raise ValueError(f"model list {path} section {section} is missing or not a list")
    refs = [str(value).strip() for value in values if str(value).strip()]
    if not refs:
        raise ValueError(f"model list {path} section {section} is empty")
    return refs


def load_state(path: pathlib.Path) -> dict[str, Any]:
    if not path.exists():
        return {"sections": {}}
    try:
        state = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise ValueError(f"invalid rotation state JSON: {path}: {exc}") from exc
    if not isinstance(state, dict):
        raise ValueError(f"rotation state must be a JSON object: {path}")
    sections = state.setdefault("sections", {})
    if not isinstance(sections, dict):
        raise ValueError(f"rotation state sections must be a JSON object: {path}")
    return state


def section_record(state: dict[str, Any], section: str) -> dict[str, Any]:
    sections = state.setdefault("sections", {})
    record = sections.setdefault(section, {})
    if not isinstance(record, dict):
        raise ValueError(f"rotation state section {section} must be a JSON object")
    return record


def select_next(refs: list[str], state: dict[str, Any], section: str) -> str:
    last_successful = str(section_record(state, section).get("last_successful") or "")
    if last_successful in refs:
        return refs[(refs.index(last_successful) + 1) % len(refs)]
    return refs[0]


def mark_success(state: dict[str, Any], section: str, model: str) -> None:
    record = section_record(state, section)
    record["last_successful"] = model
    record["updated_at"] = dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat()


def write_state(path: pathlib.Path, state: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp_name: str | None = None
    try:
        with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as handle:
            temp_name = handle.name
            json.dump(state, handle, ensure_ascii=False, indent=2, sort_keys=True)
            handle.write("\n")
        os.replace(temp_name, path)
        temp_name = None
    finally:
        if temp_name is not None:
            try:
                os.unlink(temp_name)
            except OSError:
                pass


if __name__ == "__main__":
    raise SystemExit(main())
