# jai findings: Contributors write-back fails — `expected Object` + read/write format asymmetry

Context: **team-map** consumes jai as an MCP server and, for the ONE scoped
write exception (project staffing), pushes the `contributors` set back to
`type = Project` tickets via jai's `jai_set` tool
(docs/projects-staffing-plan.md §5.1, §9; spec.md §; deploy/README.md). This
document records a write-back failure observed in prod, its root cause **in
jai**, and a recommended fix — including making jai's contributors **read and
write formats consistent**.

**This is not a team-map bug.** team-map reads exactly what jai serialized and
writes exactly the value shape `jai_set` documents. The defect is entirely in
jai's write path.

---

## 1. Symptom (prod)

The outbox flush that drains queued contributor pushes fails on every attempt:

```
ERROR outbox push to jira failed (row held in error) trigger=manual project_id=553
  kind=contributors err="jiramcp: tool \"jai_set\" returned an error:
  setting contributors on ROX-36454:
  jira PUT /rest/api/3/issue/ROX-36454:
  400 {\"errorMessages\":[],\"errors\":{\"customfield_10466\":\"expected Object\"}}"
WARN outbox flush finished with unpushed rows trigger=manual queued=1 pushed=0 failed=1 conflicts=0
```

- `customfield_10466` is Jira's **Contributors** field: an **array of user
  objects**.
- `expected Object` means Jira received an array whose **elements are bare
  strings** where each element must be a **user object** (e.g.
  `{"accountId":"…"}`).
- The row is flipped to `error` and retained; no contributors ever reach Jira.

---

## 2. Root cause (in jai)

### 2.1 Write path never wraps user-array elements

`internal/mcp/tools_write.go` → `setField`, `operation="set"`,
`fieldType=="array"` (around lines 414–424):

```go
items := parseArrayValue(value)           // "a@x,b@x" → ["a@x","b@x"]
wrapped := make([]interface{}, len(items))
for i, item := range items {
    if w, ok := wrapArrayItemValue(jiraID, item); ok {
        wrapped[i] = w
    } else {
        wrapped[i] = item                 // ← Contributors lands here: raw string
    }
}
payloadVal = wrapped
```

`wrapArrayItemValue` (lines 649–655) only knows two fields:

```go
func wrapArrayItemValue(jiraID, value string) (result interface{}, ok bool) {
    switch jiraID {
    case "components", "fixVersions":
        return map[string]string{"name": value}, true
    }
    return nil, false   // customfield_10466 → ok=false → bare email string sent
}
```

So jai PUTs `customfield_10466: ["a@x","b@x"]` (array of **strings**). Jira
requires an array of **objects** → `400 … "expected Object"`.

The same gap exists in the bulk path (`setBulk`, lines 512–522) — identical
`wrapArrayItemValue` fallthrough.

### 2.2 Two compounding problems, not one

1. **Object shape.** User-array elements must be objects, not strings.
2. **Identity resolution.** team-map (and jai's own read column) key on
   **email**, but Jira Cloud rejects emails inside the object — it requires
   `accountId`. jai already does this resolution for the scalar `assignee` /
   `reporter` fields (`wrapScalarFieldValue`, lines 635–647, via
   `jiraClient.ResolveAccountID`), but there is **no equivalent for user
   arrays**. Wrapping alone (`{"emailAddress":"a@x"}`) would still fail on
   Jira Cloud.

### 2.3 jai's read path already handles user arrays — write is asymmetric

`internal/sync/denormalize.go` (array case, ~lines 205–242) correctly reads a
Contributors user-object array and extracts `emailAddress` (falling back to
`displayName`, then `accountId`), storing the column as a JSON array of emails.
jai can turn user objects → emails on read, but has **no inverse** (emails →
user objects) on write.

---

## 3. The read/write format asymmetry (jai exposes two representations)

The same field is exposed to consumers in **two different shapes** by jai:

| Direction | jai surface | Format jai uses | jai code |
|---|---|---|---|
| Read | SQL column `contributors` (via query) | **JSON array of emails** `["a@x","b@x"]` | `internal/sync/denormalize.go` (array→emailAddress) |
| Write | `jai_set` tool `value` param | **comma-delimited string** `a@x,b@x`, split server-side | `internal/mcp/tools_write.go` `parseArrayValue` |

A consumer that reads the column gets JSON; to write the same set back it must
re-encode as a comma string. The two shapes look alike (`["a@x","b@x"]` vs
`a@x,b@x`) but live at different layers and are **not interchangeable**:

- Passing the JSON string `["a@x","b@x"]` as `jai_set value` corrupts the data:
  `parseArrayValue` comma-splits it into `["a@x"`, `"b@x"]` (brackets/quotes
  attached).
- Passing a comma string into the read column would not parse as the JSON array
  jai wrote.

This asymmetry is a jai design smell. team-map cannot unify it unilaterally
because each end is fixed by jai.

---

## 4. How team-map uses this data (requirements jai must satisfy)

team-map is a client of both jai surfaces. The contract it depends on:

### 4.1 Read (pull)

- Query: `SELECT key, …, contributors, … FROM issues WHERE type = 'Project'
  ORDER BY key ASC` (`internal/ingest/sync.go:60`, `projectSyncSQL`).
- Parse: `internal/ingest/util.go:37-55` `parseContributors` — tries JSON array
  first (`["a@x","b@x"]`), falls back to comma-split for older/hand-entered
  data.
- **team-map keys engineers strictly by email.** `contributorEmails`
  (`sync.go:325-333`) unions the ticket assignee (Lead) with every email in the
  `contributors` cell, resolves each to a local `engineer` row **by email**, and
  writes `project_assignment` pairings with `source = sync:jira`.
- Empty/unresolved contributors are visible in prod as
  `contributors_jira_hash = sha256("")` (docs/prod-postgres-debugging.md) — the
  sentinel for "sync resolved zero contributor emails".

**Requirement:** the read column MUST remain a stable, delimited list of
**emailAddress** (fall back to accountId/displayName only when email is hidden).
This is the identity team-map joins on; changing it to display names or opaque
IDs breaks assignment resolution.

### 4.2 Write (push)

- team-map enqueues an `assignment_outbox` row (`kind=contributors`) carrying
  the **full desired set of emails** plus a drift anchor hash
  (`internal/ingest/push.go`, `ContributorsPayload`).
- The flush worker calls the scoped writer `SetContributors`
  (`internal/jiramcp/write.go:47-48`), which joins the emails with commas and
  calls `jai_set` with `{keys, field:"contributors", value:"a@x,b@x",
  operation:"set"}` (`write.go:80-85`).
- A **full-set replace** (`operation=set`) is intentional — it makes the push
  idempotent (re-pushing the same set is a Jira no-op) and is the anchor for the
  three-way merge (`contributors_synced_hash`).
- Field scope is allowlisted in team-map (`internal/jiramcp/write.go`,
  `allowedWriteFields` = `contributors`, `target_start`, `target_end`); jai is
  never asked to write anything else, and the agent never receives the writer.

**Requirement:** `jai_set` MUST accept the desired contributor set expressed as
**emails** and resolve them to Jira users itself (team-map has emails, not
accountIds). team-map does not want to run its own Jira user-lookup — that is
jai's role as the Jira client (it already does it for `assignee`).

---

## 5. Suggested solution (in jai)

Two fixes; (A) is the correctness fix for the 400, (B) is the consistency fix
you also asked for.

### A. Wrap user-array items as resolved user objects (fixes the 400)

For array fields whose element type is **user** (Contributors,
`customfield_10466`, and any other user-array custom field), jai must, for each
item:

1. Resolve the identifier to a Jira `accountId` (reuse
   `jiraClient.ResolveAccountID`, already used for scalar `assignee`/`reporter`).
   Accept an email (team-map's input) or an existing accountId.
2. Wrap as `{"accountId": "<resolved>"}`.
3. Send the array of those objects to `UpdateField`.

Apply the same treatment in **both** `setField` and `setBulk`.

The blocker: `FieldMapping.Type` is only `"array"` (`internal/db/fields.go:12`)
with **no element-type**, so jai cannot today distinguish a user array from a
component/option array by type alone. Options:

- Persist an element/sub-type in the field map during schema sync (Jira's field
  schema exposes `schema.items` = `user` / `component` / `version` / `option`),
  and switch `wrapArrayItemValue` on it. **Preferred** — general and future-proof.
- Or, as a narrow stopgap, special-case known user-array custom field IDs.
  Fragile; avoid if the schema sub-type is available.

Mirror the existing **read** logic (`denormalize.go` already knows Contributors
is a user array) so read and write agree on element typing.

### B. Make contributors read and write formats consistent (requested)

Pick ONE canonical wire format for the field and use it on both ends so a
consumer round-trips without re-encoding. Two acceptable directions:

- **Preferred — accept a JSON array on write.** Let `jai_set value` (for array
  fields) accept a JSON array string (`["a@x","b@x"]`) in addition to the
  comma form, matching exactly what the read column emits. A consumer can then
  read the column and pass it straight back. Keep comma as a backward-compatible
  fallback.
- **Or — comma on both ends.** Serialize the read column as a comma-delimited
  string too. Simpler, but loses JSON-array robustness (emails never contain
  commas, so this is safe for *this* field) and is a read-format change that
  affects existing consumers.

Whichever is chosen, **document the canonical format** in the `jai_set` tool
schema (`value` description) and in the field/column docs so read and write are
explicitly the same shape.

> Note: format consistency (B) is **orthogonal** to the 400. Even with unified
> formats, the object-wrapping + accountId resolution (A) is still required —
> the elements must become user objects regardless of whether the input arrived
> as comma or JSON.

---

## 6. Acceptance / verification

A fix is complete when:

1. `jai_set keys=ROX-36454 field=contributors value="a@x,b@x" operation=set`
   PUTs `customfield_10466: [{"accountId":"…"},{"accountId":"…"}]` and Jira
   returns 2xx (no `expected Object`).
2. Emails that Jira can resolve are accepted directly (team-map sends emails).
   An unresolvable email is a clear, per-item error — not a silent drop.
3. Round-trip holds: the set written via `jai_set` reads back through the
   `contributors` column as the same emails team-map originally pushed
   (`ContributorsHash` stable).
4. Bulk path (`setBulk`) behaves identically.
5. Read column format is unchanged for consumers (still emails), OR, if (B) is
   implemented, the read and `jai_set` formats are documented as identical and
   both are accepted.

### Repro (live)

```bash
# Contributors present live in Jira (array of user objects)
jai get ROX-36454 --raw | jq '.fields.customfield_10466 | map(.emailAddress)'

# Failing write today (bare strings → 400 expected Object)
jai set ROX-36454 contributors "jvmartin@redhat.com,ksanchet@redhat.com" --operation=set

# After fix: 2xx, and read-back matches
jai query "SELECT contributors FROM issues WHERE key='ROX-36454'" --no-sync
```

---

## 7. Summary for the jai fix

1. **Correctness (the 400):** user-array fields (Contributors,
   `customfield_10466`) must be written as an array of **user objects with a
   resolved `accountId`**, not bare strings. Reuse `ResolveAccountID`; apply in
   `setField` and `setBulk`. Requires an element/sub-type in the field map (or a
   narrow user-array special-case).
2. **Consistency (requested):** unify jai's contributors **read** (JSON array of
   emails) and **write** (`jai_set value`) formats — preferably by having
   `jai_set` accept the same JSON array the read column emits — and document the
   canonical shape.
3. **team-map contract to preserve:** read column stays keyed on **email**;
   `jai_set` accepts **emails** and resolves them itself; `operation=set`
   full-replace remains idempotent. team-map requires no change.
