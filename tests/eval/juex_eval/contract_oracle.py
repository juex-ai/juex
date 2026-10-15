from __future__ import annotations

import json
import pathlib
import re
from dataclasses import dataclass
from typing import Any


@dataclass
class ContractReport:
    passed: bool
    issues: list[str]

    def message(self) -> str:
        if not self.issues:
            return ""
        return ", ".join(self.issues)


def validate_agent_smoke_contract(conversation: pathlib.Path, events: pathlib.Path, token: str) -> ContractReport:
    issues: list[str] = []
    ok, detail = conversation_has_agent_smoke_tools(conversation, token)
    if not ok:
        issues.append(detail)
    ok, detail = events_have_agent_smoke_terminal_results(events, token)
    if not ok:
        issues.append(detail)
    return ContractReport(passed=not issues, issues=issues)


def conversation_has_agent_smoke_tools(path: pathlib.Path, token: str) -> tuple[bool, str]:
    if not path.is_file() and not path.is_dir():
        return False, "missing Thread Journal"
    tool_uses: dict[str, str] = {}
    seen_tools: set[str] = set()
    legacy_uses: list[str] = []
    saw_tty_exec = False
    saw_write_stdin = False
    saw_exec_result = False
    issues: list[str] = []
    messages = load_thread_journal_records(path, "message", issues)
    if issues:
        return False, issues[0]
    for line_number, message in enumerate(messages, start=1):
        blocks = message.get("blocks")
        if not isinstance(blocks, list):
            continue
        for block in blocks:
            if not isinstance(block, dict):
                continue
            block_type = block.get("type")
            if block_type == "tool_use":
                tool_name = block.get("tool_name")
                if isinstance(tool_name, str):
                    seen_tools.add(tool_name)
                    tool_use_id = str(block.get("tool_use_id") or "")
                    if tool_use_id:
                        tool_uses[tool_use_id] = tool_name
                input_value = block.get("input")
                if tool_name == "exec_command":
                    saw_tty_exec = saw_tty_exec or (isinstance(input_value, dict) and input_value.get("tty") is True)
                if tool_name == "write_stdin":
                    saw_write_stdin = True
                if tool_name in {"shell", "shell_input"}:
                    legacy_uses.append(f"{line_number}:{tool_name}")
            elif block_type == "tool_result":
                tool_use_id = str(block.get("tool_use_id") or "")
                content = str(block.get("content") or "")
                if tool_uses.get(tool_use_id) == "exec_command" and token in content and "Process exited with code 0" in content:
                    saw_exec_result = True
    if legacy_uses:
        return False, "Thread Journal contains unprojected shell tool_use: " + ", ".join(legacy_uses)
    required = {"read", "write", "edit", "grep", "exec_command", "write_stdin"}
    missing = sorted(required - seen_tools)
    if missing:
        return False, "missing required tool_use blocks: " + ", ".join(missing)
    if not saw_tty_exec:
        return False, "missing exec_command tool_use with tty:true"
    if not saw_write_stdin:
        return False, "missing write_stdin tool_use"
    if not saw_exec_result:
        return False, f"missing exec_command tool_result containing {token} and successful exit status"
    return True, ""


def events_have_agent_smoke_terminal_results(path: pathlib.Path, token: str) -> tuple[bool, str]:
    if not path.is_file() and not path.is_dir():
        return False, "missing Thread Journal"
    delta_count = 0
    saw_install = False
    saw_prompt = False
    saw_done = False
    saw_carriage_return = False
    saw_write_stdin_completed = False
    saw_structured_exec_running = False
    saw_structured_write_stdin_result = False
    issues: list[str] = []
    events = load_thread_journal_records(path, "event", issues)
    if issues:
        return False, issues[0]
    for event in events:
        payload = event.get("payload")
        if not isinstance(payload, dict):
            continue
        if event.get("type") == "tool.output_delta":
            delta_count += 1
        if event.get("type") == "tool.completed":
            result = payload.get("result")
            content = _terminal_content(payload)
            saw_install = saw_install or "INSTALL" in content
            saw_prompt = saw_prompt or "PROMPT approve install" in content
            saw_done = saw_done or f"TTY-DONE {token}" in content
            saw_carriage_return = saw_carriage_return or "\r" in content
            if payload.get("name") == "exec_command" and isinstance(result, dict):
                session_id = result.get("session_id")
                saw_structured_exec_running = saw_structured_exec_running or (
                    result.get("running") is True and _number_not_bool(session_id) and session_id > 0
                )
            if payload.get("name") == "write_stdin":
                saw_write_stdin_completed = True
                if isinstance(result, dict):
                    exit_code = result.get("exit_code")
                    saw_structured_write_stdin_result = saw_structured_write_stdin_result or (
                        result.get("running") is False
                        and _number_not_bool(exit_code)
                        and exit_code == 0
                        and f"TTY-DONE {token}" in content
                    )
    if delta_count != 0:
        return False, f"events persisted {delta_count} transient tool.output_delta records"
    missing = []
    if not saw_install:
        missing.append("INSTALL progress")
    if not saw_prompt:
        missing.append("interactive prompt")
    if not saw_done:
        missing.append("TTY-DONE token")
    if not saw_carriage_return:
        missing.append("carriage-return progress update")
    if not saw_write_stdin_completed:
        missing.append("write_stdin completion")
    if not saw_structured_exec_running:
        missing.append("structured exec_command running result")
    if not saw_structured_write_stdin_result:
        missing.append("structured write_stdin result")
    if missing:
        return False, "terminal events missing " + ", ".join(missing)
    return True, ""


def load_thread_journal_records(
    path: pathlib.Path,
    field: str,
    issues: list[str],
) -> list[dict[str, Any]]:
    """Read messages or events from registered Generation Journals.

    Direct JSONL records remain accepted for the deterministic eval harness,
    which builds in-memory oracle views rather than runtime persistence.
    """
    paths = thread_journal_paths(path)
    if not paths:
        issues.append(f"missing Thread Journal: {path}")
        return []
    records: list[dict[str, Any]] = []
    for journal in paths:
        try:
            lines = journal.read_text(encoding="utf-8", errors="replace").splitlines()
        except OSError as exc:
            issues.append(f"read Thread Journal: {exc}")
            continue
        for line_number, line in enumerate(lines, start=1):
            if not line.strip():
                continue
            try:
                value = json.loads(line)
            except json.JSONDecodeError as exc:
                issues.append(f"Thread Journal {journal.name} line {line_number} is invalid JSON: {exc}")
                continue
            if not isinstance(value, dict):
                issues.append(f"Thread Journal {journal.name} line {line_number} must be a JSON object")
                continue
            framed = value.get("data")
            if isinstance(framed, dict):
                value = framed
            facts = value.get("facts")
            if not isinstance(facts, list):
                records.append(value)
                continue
            for fact in facts:
                if not isinstance(fact, dict):
                    continue
                record = fact.get(field)
                if isinstance(record, dict):
                    records.append(record)
    return records


def thread_journal_paths(path: pathlib.Path) -> list[pathlib.Path]:
    if path.is_file():
        return [path]
    generation_dir = path / "generations"
    if not generation_dir.is_dir():
        return []
    metadata_path = path / "thread.json"
    if not metadata_path.is_file():
        return []
    try:
        metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
        generations = metadata.get("generations")
        if not isinstance(generations, list):
            return []
        registered: list[pathlib.Path] = []
        for generation in generations:
            if not isinstance(generation, dict):
                return []
            generation_id = generation.get("generation_id")
            if (
                not isinstance(generation_id, str)
                or re.fullmatch(r"g[0-9]{6}", generation_id) is None
            ):
                return []
            registered.append(generation_dir / f"{generation_id}.jsonl")
        return registered
    except (OSError, json.JSONDecodeError):
        return []


def _terminal_content(payload: dict[str, Any]) -> str:
    content = payload.get("content")
    if isinstance(content, str) and content:
        return content
    outcome = payload.get("outcome")
    if not isinstance(outcome, dict):
        return content if isinstance(content, str) else ""
    block = outcome.get("block")
    if not isinstance(block, dict):
        return content if isinstance(content, str) else ""
    recorded = block.get("content")
    return recorded if isinstance(recorded, str) else ""


def _number_not_bool(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)
