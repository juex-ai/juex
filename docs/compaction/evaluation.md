# Context Generation Evaluation

> English | [中文](evaluation.zh.md)

The live evaluation checks that a long-running Main Thread can compact under
context pressure and continue without losing explicitly protected facts or
Thread working state.

Run it through the final verification tier:

```bash
make verify-final RACE=1 WEB=1 COMPACTION=1
```

For a focused local run:

```bash
tests/eval/compaction_eval.sh --only provider:model
```

## Scenario

The harness uses one isolated Agent and its Main Thread `0` for three Inputs:

1. seed six recall facts and enough noise to approach the configured window;
2. add more pressure and require automatic compaction;
3. ask the model to reproduce the protected facts and authoritative state.

The protected path fact is
`/workspace/project/.juex/threads/0/journal.jsonl`. After the first Input, the
scenario appends valid `goal.updated` and `notes.updated` facts while the
resident Runtime is stopped.

## Pass contract

A passing result requires:

- at least one `context.compacted` fact in the Thread Journal;
- the six protected facts remain recallable;
- the compact summary contains Goal description, acceptance, and status;
- unfinished Notes appear in the summary's next steps;
- the projected Notes remain byte-identical and both completed/open Notes are recited;
- no Tool use or invented merge claim appears;
- every `juex send --wait` command settles successfully;
- the resident Runtime started by `send` is stopped after each Input;
- cached/input token ratio is reported from journal `usage.recorded` facts when
  the Provider supplies it.

The selected Provider/model, selection seed, redacted config hash, command
logs, `journal.jsonl`, `thread.json`, scorecard, and normalized
outcome are copied to `.tmp/reports/compaction-eval/<run-id>/`.

Provider unavailability and environment failures are reported separately from
product-quality failures. The gate never silently switches to an unselected
model.
