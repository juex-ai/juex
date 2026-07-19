from __future__ import annotations

import json
import pathlib
from dataclasses import dataclass
from typing import Any

try:
    from . import contract_oracle
except ImportError:  # pragma: no cover - direct script fallback.
    import contract_oracle  # type: ignore[no-redef]


FORBIDDEN_TOOLS = {
    "observable_create",
}

@dataclass(frozen=True)
class ScheduleRoutingExpectation:
    schedule_id: str
    every_seconds: int
    content: str
    completion_token: str


@dataclass(frozen=True)
class _ToolUse:
    name: str
    tool_use_id: str
    input: Any
    message_index: int
    block_index: int


@dataclass(frozen=True)
class _ToolResult:
    tool_use_id: str
    is_error: bool
    message_index: int
    block_index: int


def build_prompt(expectation: ScheduleRoutingExpectation) -> str:
    hours = expectation.every_seconds / 3600
    hours_text = str(int(hours)) if hours.is_integer() else f"{hours:g}"
    return "\n".join(
        [
            "Create recurring timed work using JueX native scheduling.",
            f"Use the fixed id {expectation.schedule_id} and run it every {hours_text} hours.",
            f"Each activation must emit observation content exactly: {expectation.content}",
            "Inspect all currently configured timed sources first, then create this one only if it is absent.",
            "Use native scheduling for recurrence; do not implement it with shell polling, background loops, or a managed command source.",
            f"After the timed work is created successfully, answer exactly: {expectation.completion_token}",
        ]
    )


def validate_contract(
    conversation_path: pathlib.Path,
    observables_path: pathlib.Path,
    expectation: ScheduleRoutingExpectation,
) -> contract_oracle.ContractReport:
    issues: list[str] = []
    messages = _load_jsonl(conversation_path, "conversation", issues)
    uses, results = _transcript_tools(messages)
    _validate_tool_contract(messages, uses, results, expectation, issues)
    _validate_persisted_config(observables_path, expectation, issues)
    return contract_oracle.ContractReport(passed=not issues, issues=issues)


def _load_jsonl(path: pathlib.Path, label: str, issues: list[str]) -> list[dict[str, Any]]:
    if not path.is_file():
        issues.append(f"missing {label} file: {path}")
        return []
    try:
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError as exc:
        issues.append(f"read {label}: {exc}")
        return []
    messages: list[dict[str, Any]] = []
    for line_number, line in enumerate(lines, start=1):
        if not line.strip():
            continue
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            issues.append(f"{label} line {line_number} is invalid JSON: {exc}")
            continue
        if not isinstance(value, dict):
            issues.append(f"{label} line {line_number} must be a JSON object")
            continue
        messages.append(value)
    return messages


def _transcript_tools(messages: list[dict[str, Any]]) -> tuple[list[_ToolUse], list[_ToolResult]]:
    uses: list[_ToolUse] = []
    results: list[_ToolResult] = []
    for message_index, message in enumerate(messages):
        blocks = message.get("blocks")
        if not isinstance(blocks, list):
            continue
        for block_index, block in enumerate(blocks):
            if not isinstance(block, dict):
                continue
            if block.get("type") == "tool_use":
                name = block.get("tool_name")
                if isinstance(name, str):
                    uses.append(
                        _ToolUse(
                            name=name,
                            tool_use_id=str(block.get("tool_use_id") or ""),
                            input=block.get("input"),
                            message_index=message_index,
                            block_index=block_index,
                        )
                    )
            elif block.get("type") == "tool_result":
                results.append(
                    _ToolResult(
                        tool_use_id=str(block.get("tool_use_id") or ""),
                        is_error=block.get("is_error") is True,
                        message_index=message_index,
                        block_index=block_index,
                    )
                )
    return uses, results


def _validate_tool_contract(
    messages: list[dict[str, Any]],
    uses: list[_ToolUse],
    results: list[_ToolResult],
    expectation: ScheduleRoutingExpectation,
    issues: list[str],
) -> None:
    completion_after: tuple[int, int] | None = None
    for use in uses:
        if use.name in FORBIDDEN_TOOLS:
            issues.append(f"forbidden tool_use: {use.name}")

    list_uses = [use for use in uses if use.name == "observable_list"]
    create_uses = [use for use in uses if use.name == "schedule_create"]
    if not list_uses:
        issues.append("expected at least one observable_list tool_use, saw 0")
    if not create_uses:
        issues.append("expected at least one schedule_create tool_use, saw 0")

    successful_creates = [
        (create_use, result)
        for create_use in create_uses
        if (result := _successful_result_after(create_use, results)) is not None
    ]
    if len(successful_creates) != 1:
        issues.append(
            f"expected exactly one successful schedule_create tool_use, saw {len(successful_creates)}"
        )

    if list_uses and len(successful_creates) == 1:
        create_use, create_result = successful_creates[0]
        list_succeeded_before_create = any(
            (result := _successful_result_after(list_use, results)) is not None
            and _position(result) < _position(create_use)
            for list_use in list_uses
        )
        if not list_succeeded_before_create:
            if any(list_use.message_index == create_use.message_index for list_use in list_uses):
                issues.append(
                    "observable_list and schedule_create cannot be in the same assistant message "
                    "without an earlier successful observable_list result"
                )
            else:
                issues.append(
                    "at least one observable_list successful tool_result must occur before schedule_create"
                )
        completion_after = _position(create_result)
        _validate_create_input(create_use.input, expectation, issues)

    if completion_after is None or not _has_exact_completion_after(
        messages,
        expectation.completion_token,
        completion_after,
    ):
        issues.append(f"exact completion token must appear after successful schedule_create: {expectation.completion_token}")


def _position(value: _ToolUse | _ToolResult) -> tuple[int, int]:
    return value.message_index, value.block_index


def _successful_result_after(use: _ToolUse, results: list[_ToolResult]) -> _ToolResult | None:
    for result in results:
        if (
            result.tool_use_id
            and result.tool_use_id == use.tool_use_id
            and _position(result) > _position(use)
        ):
            return None if result.is_error else result
    return None


def _validate_create_input(value: Any, expectation: ScheduleRoutingExpectation, issues: list[str]) -> None:
    if not isinstance(value, dict):
        issues.append("schedule_create input must be an object")
        return
    if value.get("id") != expectation.schedule_id:
        issues.append(f"schedule_create id must equal {expectation.schedule_id!r}")
    interval = value.get("interval")
    if not isinstance(interval, dict) or interval.get("every_seconds") != expectation.every_seconds:
        issues.append(f"schedule_create interval.every_seconds must equal {expectation.every_seconds}")
    observation = value.get("observation")
    if not isinstance(observation, dict) or observation.get("content") != expectation.content:
        issues.append(f"schedule_create observation.content must equal {expectation.content!r}")


def _has_exact_completion_after(
    messages: list[dict[str, Any]],
    token: str,
    after: tuple[int, int],
) -> bool:
    for message_index, message in enumerate(messages):
        if message.get("role") != "assistant":
            continue
        blocks = message.get("blocks")
        if not isinstance(blocks, list):
            continue
        for block_index, block in enumerate(blocks):
            if (
                (message_index, block_index) > after
                and isinstance(block, dict)
                and block.get("type") == "text"
                and isinstance(block.get("text"), str)
                and block["text"].strip() == token
            ):
                return True
    return False


def _validate_persisted_config(
    path: pathlib.Path,
    expectation: ScheduleRoutingExpectation,
    issues: list[str],
) -> None:
    if not path.is_file():
        issues.append(f"missing observables config: {path}")
        return
    try:
        value = json.loads(path.read_text(encoding="utf-8", errors="replace"))
    except json.JSONDecodeError as exc:
        issues.append(f"observables config is invalid JSON: {exc}")
        return
    except OSError as exc:
        issues.append(f"read observables config: {exc}")
        return
    if not isinstance(value, dict):
        issues.append("observables config must be a JSON object")
        return
    observables = value.get("observables")
    if not isinstance(observables, list):
        issues.append("observables config must contain an observables array")
        return
    if len(observables) != 1:
        issues.append(f"observables config must contain exactly one entry, saw {len(observables)}")
        return
    entry = observables[0]
    if not isinstance(entry, dict):
        issues.append("persisted observable entry must be an object")
        return
    if entry.get("id") != expectation.schedule_id:
        issues.append(f"persisted id must equal {expectation.schedule_id!r}")
    if entry.get("type") != "schedule":
        issues.append("persisted type must equal 'schedule'")
    allowed_fields = {"id", "name", "type", "schedule_config"}
    unexpected_fields = sorted(set(entry) - allowed_fields)
    if unexpected_fields:
        issues.append("persisted entry contains legacy or unknown fields: " + ", ".join(unexpected_fields))
    schedule_config = entry.get("schedule_config")
    if not isinstance(schedule_config, dict):
        issues.append("persisted entry must contain schedule_config")
        return
    interval = schedule_config.get("interval")
    if not isinstance(interval, dict) or interval.get("every_seconds") != expectation.every_seconds:
        issues.append(f"persisted schedule_config.interval.every_seconds must equal {expectation.every_seconds}")
    observation = schedule_config.get("observation")
    if not isinstance(observation, dict) or observation.get("content") != expectation.content:
        issues.append(f"persisted schedule_config.observation.content must equal {expectation.content!r}")
