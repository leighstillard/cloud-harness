---
name: harness
description: Persistent cross-session memory via the `harness` binary. Use when a decision, constraint, gotcha, or user preference is worth keeping for future sessions, when the user says "remember this", or when the `## harness` block at the top of context needs interpreting.
---

# harness memory

The `## harness` block printed at session start is memory pulled from the memory provider. Treat it as true unless the code in front of you contradicts it.

## When to run `harness remember`

Run it when you learn something a future session in this repo (or any repo) would otherwise have to rediscover:

- A decision and its reason ("we use X instead of Y because Z").
- A constraint that is not obvious from the code (release process, env quirks, who owns what).
- A gotcha that cost time (a flaky command, a path that must exist, an order of operations).
- A user preference about how work should be done.

Do not run it for things the repo already states (README, CLAUDE.md, comments), for transient task state, or for anything the user asked you to keep private.

## How

```
harness remember "<one fact, one sentence, present tense>"
harness remember --global "<fact that applies in every repo>"
```

One fact per call. Write it so it reads correctly with no surrounding context. Prefer "Use `make test` before pushing; CI runs the same target" over "tests passed".

## Scoping rules

- Default scope is the current repo (`owner/name` from the git remote). Use it for anything about this codebase.
- `--global` scope is for the user, not the repo: tooling preferences, writing style, cross-repo conventions. Be sparing.
- Never write secrets, tokens, hostnames of private infrastructure, or personal data to memory. Memory lives in a third-party service.

## Auto capture

The Stop and PreCompact hooks run `harness remember --auto`. It stores only lines in your last message that start with `Remember:`. If you want a fact captured without a separate command, end a message with a line such as:

```
Remember: the integration tests need `docker compose up db` first.
```
