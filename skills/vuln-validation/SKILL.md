---
name: vuln-validation
description: Validate source-code, native binary, parser, protocol, memory-safety, and logic-bug hypotheses with tiered executable harnesses, fuzzing, sanitizers, or full environments.
---

# Vulnerability validation ladder

Use this skill when the assigned intent investigates source code, native binaries,
parsers, protocols, arithmetic, memory safety, or a suspected logic flaw that needs
an executable proof. The exploration graph is the append-only journal: read the
parent evidence, execute one bounded validation tier, and write the result back
before stopping.

## Evidence contract

A static code observation is an **inferred hypothesis**, not a vulnerability.
Confirmation requires an executable harness that reaches the suspected operation
and produces a security-relevant effect or invariant violation. A version match,
code pattern, crash without reachability, or sanitizer warning unrelated to the
hypothesis does not confirm it.

Before execution, state five facts in the working notes:

1. Attacker-controlled input and realistic entry point.
2. Exact function, call chain, arithmetic, state transition, or invariant under test.
3. Expected vulnerable signal and expected safe/control behavior.
4. Smallest environment capable of exercising that operation.
5. Evidence needed to advance to the next tier.

## Validation ladder

Stop at the first tier that proves or refutes the primitive. Escalate only when the
current tier cannot represent a prerequisite that matters.

### Tier 1 — isolated function

Build the smallest standalone reproducer around one function or narrow unit. Stub
unrelated dependencies and preserve the real types, boundary arithmetic, and state
that control the suspected operation. Prefer a deterministic table sweep or focused
fuzzer over random broad fuzzing.

For native code, use the project's existing build system when possible and enable
appropriate sanitizers such as ASan, UBSan, or MSan. libFuzzer/AFL++ is useful only
when the harness exposes the real input boundary and records the triggering corpus.

Completion criterion: coverage or instrumentation proves the suspected branch ran,
and either a minimized triggering input plus control reproduces the primitive, or
the exact bounded hypothesis is falsified.

### Tier 2 — multi-component

Use only after Tier 1 establishes a primitive whose reachability depends on a real
caller, parser stage, allocator behavior, serializer, protocol transition, or other
component interaction. Add the minimum components needed; do not jump to the full
application.

Completion criterion: the real component boundary reaches the proven primitive with
attacker-controlled data, with a reproducible command and saved artifacts.

### Tier 3 — full environment

Use a full build, service, VM, kernel, device, or end-to-end deployment only when it
is necessary to prove production reachability or the final impact. Carry the
minimized Tier 1/2 trigger forward instead of restarting exploration in the large
environment.

Completion criterion: the trigger reproduces in the representative environment, or
the environment provides concrete evidence that a prerequisite is absent.

## Controls and minimization

- Pair every positive run with a nearby safe input or patched/expected path.
- Minimize the input after reproduction; keep the original and minimized artifacts.
- Record compiler flags, sanitizer options, seed/corpus, resource limits, revision,
  and exact run command.
- If memory or timing limits mask the trigger, reduce harness overhead before
  changing the hypothesis. Record the masking condition as evidence.
- Treat a timeout, OOM, tool failure, or unavailable VM as an environment result,
  not a negative vulnerability result.
- Full exploit development is separate from primitive validation. Attempt it only
  when the task explicitly requires it and authorization permits it.

## Write-back

Use `record_fact` for each tier's observed result and link it to the assigned intent.
Include the exact command, minimized input or artifact path, coverage/reachability
proof, sanitizer/crash output, control result, and confidence. If another tier is
needed, record the missing prerequisite so the planner can create one child intent
with this fact as `parent_id`.

Use `report_finding` only after an executable tier confirms the vulnerability with a
reproducible proof. State the highest completed tier and do not claim end-to-end
impact from an isolated primitive alone.
