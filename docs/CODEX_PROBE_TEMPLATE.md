# Codex ticket and degradation check template

Version 1.1.4 shares one JSONL initial context between automatic ticket harvesting,
manual harvesting and degradation checks, including models
that do not need tickets. Edit **Codex ticket and degradation check template** in
the administrator's gateway settings and save the page. **Restore default** loads
the built-in template into the editor; save to apply it.

The [default JSONL](../backend/internal/service/codex_probe_template.jsonl) contains
English static instructions and anonymous paths. Memory and skill text contains
no personal history. Paths are prompt text only. The production service does not
read a local Codex recording or installation file.

Keep the seven records, five message roles and content segmentation, prompt
wrappers and environment XML hierarchy and attributes. The environment has three
workspace roots and eighteen permission entries. Only initial developer/user
`input_text` messages are accepted; assistant output, tools, reasoning and extra
recording metadata are rejected. Templates can be at most 256 KiB.

| Placeholder | Value |
| --- | --- |
| `{{TIMEZONE}}` | Target account's request timezone; default `Asia/Singapore` |
| `{{CURRENT_DATE}}` | Request start date in that timezone, `YYYY-MM-DD` |
| `{{MODEL}}` | Target model |
| `{{MODELTRACE_PROMPT}}` | Fresh ModelTrace challenge for this actual request |

The challenge placeholder must occur exactly once, as the entire final user
message. Challenges retain the existing Chinese instructions and expected-count
validation. Replacement happens after parsing, followed by JSON encoding, so
quotes and newlines remain valid. Unknown placeholders are rejected with a line
number and a reason that does not include template text.

Administrator settings GET and PUT use `openai_codex_ticket_prompt_template`.
GET returns the effective template and the read-only
`openai_codex_ticket_prompt_template_default`. Omit the PUT field to keep its
stored value; send an empty string to use the built-in default. Invalid templates
are rejected before saving. The saving process invalidates its cache immediately;
other instances refresh within the five-second cache lifetime. Each diagnostic
model pins one snapshot for its check. The next model can
load newer settings. Audits record the changed field name, never the template.
Public settings do not expose either template field.

Initial installation identity is a stable UUIDv4 derived from the existing account
credential namespace. Each request creates fresh UUIDv7 session/thread, window and
turn identities shared by headers and body. Diagnostics still enter the normal
`/v1/responses` gateway with the selected billing API key, target account and
normal account isolation and fingerprint handling. Checks do not preflight ticket
availability, harvest tickets, or recheck ticket generations after a response.
The gateway applies the same ticket policy and rate limits as ordinary requests,
and gateway errors produce a failed check. Harvesting retains its
production proxy, scheduling and ticket validation. Invalid runtime templates
produce `template_invalid` and skip the model request; this is a configuration
failure, not a model degradation result.

Version 1.1.4 defaults `gateway.openai_codex_ticket.enabled` to `false`: enable
ticket harvesting manually in administrator settings when needed. Migration 244
turns the global switch off once on upgrade, including previously enabled or
unset values. Later administrator choices survive restarts. Account and
model participation choices apply when harvesting is enabled. The default for
`gateway.openai_codex_ticket.fail_closed` remains `false`, allowing normal requests
without a ticket. When ticket handling is enabled, valid saved tickets are injected.
Missing tickets do not block scheduling or forwarding when global harvesting is
disabled or either the account or the actual outbound model has opted out of
harvesting. Re-enabling participation restores the saved policy. When global
harvesting, account participation and model participation are all enabled,
policy priority remains: explicit account override, saved
global setting, YAML/environment configuration, built-in default. Existing
missing-ticket policies and account/model participation choices are preserved;
the upgrade only resets the global harvesting switch.

Local tests verify configuration and request behavior with simulated upstream
responses. Actual ticket yield and diagnostic effectiveness require real-account
experiments.
