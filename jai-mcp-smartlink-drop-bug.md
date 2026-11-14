# jai MCP bug: ADF description drops smart-links / hyperlinks

## Prompt (for the jai maintainer / agent fixing jai)

> The jai **MCP** tools drop link URLs when converting a Jira ADF `description` to text/markdown.
> The `jai` **CLI** does not — so the bug is in the MCP layer's ADF→markdown conversion, not the data.
>
> **Repro:** `ROX-36990` has a description whose only content is `See project document:` followed
> by a Google-Doc smart-link.
> - `mcp__jai__jai_get(key="ROX-36990", fields="all")` → `description: "See project document:"`
>   (URL gone). Same via `jai_query(jql="key=ROX-36990", fields="description")` → `null`/truncated.
> - `jai get ROX-36990` (CLI) → returns the full text **including**
>   `https://docs.google.com/document/d/1gXyX.../edit` .
>
> **Fix:** make the MCP ADF→markdown converter preserve link targets for every ADF node type
> that carries a URL, emitting standard markdown links:
> - inline `link` marks on text → `[text](href)`
> - `inlineCard` / `blockCard` / `embedCard` (smart-links, e.g. Google Docs, Confluence) →
>   `[url](url)` (or `[title](url)` when the card's title/attrs are available)
> - bare `text` nodes that are URLs → keep the URL verbatim
>
> Apply the same conversion everywhere a description/comment/rich-text field is rendered
> (`jai_get`, `jai_query` field output, any `fields="all"` path). Add a regression test using an
> ADF doc containing an `inlineCard` and a `link` mark, asserting both URLs survive in the
> markdown output. Round-trip parity with `jai get` (CLI) is the acceptance bar.
>
> **Why it matters:** downstream automation reads a `Project` ticket's description to find its
> linked shape doc (Google Doc). With the URL dropped, the doc is invisible and the automation
> can't detect that a Project already covers a feature — leading to duplicate `Project` tickets.

## Impact / workaround
- Impact: automated existing-Project detection can't see a Project's linked design doc, causing
  duplicate Projects (e.g. `ROX-37038` created while `ROX-36990` already incorporated `ROX-35712`).
- Workaround until fixed: when an MCP description reads `See project document:` with no URL, fall
  back to the `jai` CLI (`jai get <key>`) to recover the smart-link, then export with
  `gog docs export`.
