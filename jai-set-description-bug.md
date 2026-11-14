# jai bug: cannot set the `description` field on Jira Cloud

## Summary

Neither the MCP tool `jai_set` nor the CLI `jai set` / `jai update --set` can write
the **`description`** field on a Jira Cloud issue. Both fail with a `400` from
`PUT /rest/api/3/issue/{key}`. Two *different* errors surface depending on the
payload, which suggests two overlapping causes: (1) `set`/`update` send the value as
a raw string and do **no** text→ADF conversion, and (2) a possible edit-screen
constraint for the `Project` issue type.

Notably, `jai comment` and `jai create` **do** convert text→ADF (their own docs say so),
so the converter already exists in jai — it just isn't wired into `set`/`update` for
rich-text fields.

## Environment

- Jira: `redhat.atlassian.net` (Jira Cloud), REST API **v3** (`PUT /rest/api/3/issue/{key}`)
- Test issue: **ROX-36440**, issue type **`Project`**
- Field mapping (`jai fields --filter description`):

  | name | jira_name | jira_id | type |
  |------|-----------|---------|------|
  | `description` | Description | `description` | `text` |

## Reproduction

### Case A — MCP `jai_set` with markdown content

```
jai_set(field="description", keys="ROX-36440", value="<large markdown document>")
```

Result:

```
400 {"errorMessages":[],"errors":{
  "description":"Operation value must be an Atlassian Document (see the Atlassian Document Format)"
}}
```

### Case B — CLI `jai set` with a short plain string

```
jai set ROX-36440 description "Test ADF conversion via CLI" --no-sync
```

Result:

```
Error: setting description on ROX-36440: jira PUT /rest/api/3/issue/ROX-36440: 400 {"errorMessages":[],"errors":{
  "description":"Field 'description' cannot be set. It is not on the appropriate screen, or unknown."
}}
```

`jai update --set description=...` uses the same scalar `field=value` path and is
expected to fail identically.

## Analysis

1. **No ADF conversion in `set`/`update` (Case A).**
   Jira Cloud REST v3 requires `description` to be an **ADF document object**, not a
   string. `jai set` / `jai_set` send the value verbatim as a string, so Jira rejects
   it with "Operation value must be an Atlassian Document."
   The converter jai already ships for `jai comment` (`text` → ADF) and `jai create`
   (`description` "plain text" → ADF) is **not** applied on the `set`/`update` path.

2. **Possible edit-screen / field-resolution issue (Case B).**
   With a short plain string, the error changes to "Field 'description' cannot be set.
   It is not on the appropriate screen, or unknown." This may mean `description` is not
   on the **Edit** screen for the `Project` issue type in this project's screen scheme,
   OR it is a secondary symptom of jai sending the wrong payload shape. This needs
   confirmation (see below).

## Diagnostics still needed

- Fetch edit metadata to see whether `description` is actually editable for this issue:
  `GET /rest/api/3/issue/ROX-36440/editmeta` — check whether `fields.description` is
  present. If it is present, cause (2) is a jai payload/format problem, not a screen
  restriction. If it is absent, the field genuinely isn't on the `Project` edit screen
  and needs a Jira admin/screen-scheme change (not a jai bug).

## Expected behavior

- `jai set <key> description <text-or-markdown>` and
  `jai update <key> --set description=<...>` should write the description successfully,
  converting the value to ADF the same way `jai comment` / `jai create` already do.

## Suggested fixes (for the jai maintainer/agent)

1. **Wire the existing text→ADF converter into `set`/`update`** for rich-text fields
   (fields whose mapping `type` is `text` and that resolve to Jira ADF fields:
   `description` and the custom `*_description` fields). Convert before the `PUT`.
2. **Add a `--file` flag** to `jai set` / `jai update` so large markdown descriptions
   can be supplied from a file, avoiding shell-escaping problems with backticks, pipes,
   and quotes.
3. **Improve error disambiguation:** when a `400` mentions "not on the appropriate
   screen," first check `editmeta` and report whether the field is editable for that
   issue type, rather than surfacing the raw generic message.
4. **Test explicitly against issue type `Project`** (e.g. ROX-36440), since screen
   schemes and editable fields differ by issue type.

## How to verify a fix

```
# Should succeed and set a rich (ADF) description:
jai set ROX-36440 description "$(cat projects/EP-ROX-36440.md)"

# Or, with the proposed flag:
jai set ROX-36440 description --file projects/EP-ROX-36440.md
```

Then confirm via `jai get ROX-36440 --fields description` (and in the Jira UI) that the
formatted content rendered.

## Context

The goal was to push `projects/EP-ROX-36440.md` (a project brief) into the ROX-36440
`description`. Current workarounds that *do* work today: `jai comment` (adds the content
as a comment, converted to ADF) or pasting the markdown into the Jira description editor
manually.
