from __future__ import annotations

import json
import pathlib
from dataclasses import dataclass


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
    ok, detail = events_have_agent_smoke_deltas(events, token)
    if not ok:
        issues.append(detail)
    return ContractReport(passed=not issues, issues=issues)


def conversation_has_agent_smoke_tools(path: pathlib.Path, token: str) -> tuple[bool, str]:
    if not path.is_file():
        return False, "missing conversation log"
    tool_uses: dict[str, str] = {}
    seen_tools: set[str] = set()
    legacy_uses: list[str] = []
    saw_tty_exec = False
    saw_write_stdin = False
    saw_exec_result = False
    try:
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError as exc:
        return False, f"read conversation: {exc}"
    for line_number, line in enumerate(lines, start=1):
        if not line.strip():
            continue
        try:
            message = json.loads(line)
        except json.JSONDecodeError as exc:
            return False, f"conversation line {line_number} is invalid JSON: {exc}"
        blocks = message.get("blocks") if isinstance(message, dict) else None
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
        return False, "conversation contains legacy shell tool_use: " + ", ".join(legacy_uses)
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


def events_have_agent_smoke_deltas(path: pathlib.Path, token: str) -> tuple[bool, str]:
    if not path.is_file():
        return False, "missing events log"
    delta_count = 0
    saw_install = False
    saw_prompt = False
    saw_done = False
    saw_carriage_return = False
    saw_write_stdin_completed = False
    saw_structured_exec_running = False
    saw_structured_write_stdin_result = False
    try:
        lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    except OSError as exc:
        return False, f"read events: {exc}"
    for line_number, line in enumerate(lines, start=1):
        if not line.strip():
            continue
        try:
            event = json.loads(line)
        except json.JSONDecodeError as exc:
            return False, f"events line {line_number} is invalid JSON: {exc}"
        payload = event.get("payload") if isinstance(event, dict) else None
        if not isinstance(payload, dict):
            continue
        if event.get("type") == "tool.output_delta":
            text = str(payload.get("text") or "")
            delta_count += 1
            saw_install = saw_install or "INSTALL" in text
            saw_prompt = saw_prompt or "PROMPT approve install" in text
            saw_done = saw_done or f"TTY-DONE {token}" in text
            saw_carriage_return = saw_carriage_return or "\r" in text
        if event.get("type") == "tool.completed":
            result = payload.get("result")
            if payload.get("name") == "exec_command" and isinstance(result, dict):
                session_id = result.get("session_id")
                saw_structured_exec_running = saw_structured_exec_running or (
                    result.get("running") is True and isinstance(session_id, (int, float)) and session_id > 0
                )
            if payload.get("name") == "write_stdin":
                saw_write_stdin_completed = True
                if isinstance(result, dict):
                    saw_structured_write_stdin_result = saw_structured_write_stdin_result or (
                        result.get("running") is False
                        and result.get("exit_code") == 0
                        and f"TTY-DONE {token}" in str(result.get("output") or "")
                    )
    if delta_count < 3:
        return False, f"expected at least 3 tool.output_delta events, saw {delta_count}"
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
        return False, "events missing " + ", ".join(missing)
    return True, ""
