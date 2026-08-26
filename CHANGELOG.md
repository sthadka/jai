# Changelog

## [4.0.0](https://github.com/sthadka/jai/compare/v3.1.0...v4.0.0) (2026-08-26)


### ⚠ BREAKING CHANGES

* `jai get` (and `jai get --json`) now returns only a curated subset of fields by default, matching the front-matter table view, instead of every column in the issues table. Scripts relying on the previous all-columns default must pass --fields all to get the old behavior back.

### Features

* add attachment metadata sync ([6357e4b](https://github.com/sthadka/jai/commit/6357e4bfe527bb5f998c58e9923ea758159a8235))
* add bulk transition support ([ea3f704](https://github.com/sthadka/jai/commit/ea3f70436e740e22d595990c8c1817baab1fe7c2))
* add changelog query interface with built-in snippets ([1ed1a6d](https://github.com/sthadka/jai/commit/1ed1a6d44677eb00869923a5aa71ebce043ccf52))
* add Claude Code skill file for jai ([c3771d4](https://github.com/sthadka/jai/commit/c3771d4760636e35f0045a8983ba2a01ab2829ce))
* add composite jai update command ([8f5f91f](https://github.com/sthadka/jai/commit/8f5f91f31e2eb915edd938eca8e557d94b61c30e))
* add cross-project dependency analysis ([ab484a2](https://github.com/sthadka/jai/commit/ab484a28c96c63f4edc7b1a0e7a45721b3ed8fce))
* add CSV/TSV/Markdown export formats ([3b458d7](https://github.com/sthadka/jai/commit/3b458d75947438d9880916135903dba4f9b66d64))
* add development info sync ([1464abe](https://github.com/sthadka/jai/commit/1464abe604849072d24c43fa1920feebc3704d8a))
* add field search and suggestion helpers plus value counts ([88a4a35](https://github.com/sthadka/jai/commit/88a4a35f19ac5c9a0e460fd353f1d0bb2d6f1fe6))
* add JQL support to MCP jai_query tool ([d32e1e1](https://github.com/sthadka/jai/commit/d32e1e1225f80455bc02be5f9a0ac8e7043726b4))
* add MCP response efficiency helpers and default field sets ([8edf582](https://github.com/sthadka/jai/commit/8edf5821051b031c4715b07f25e2e1e0eb5eadc2))
* add MCP server core with stdio transport ([f7756a3](https://github.com/sthadka/jai/commit/f7756a37392a65303d72f58398e36e430ca02893))
* add migration 10 for agent integration tables ([62ac12e](https://github.com/sthadka/jai/commit/62ac12edcfa567e2518699c07af19dd2286cccdc))
* add non-blocking auto-sync with background worker ([a234a3d](https://github.com/sthadka/jai/commit/a234a3df61393bd5b6f349db3f3842cb06fbe7a0))
* add sprint and board data sync from Jira Agile API ([81a0d43](https://github.com/sthadka/jai/commit/81a0d438b29f6ea1890660a58b995813067e5b3d))
* add templates and snippets library ([6146cd0](https://github.com/sthadka/jai/commit/6146cd0ec068a3534449c904e0372b3066fd59d5))
* add tiered schema discovery for MCP token efficiency ([86347fd](https://github.com/sthadka/jai/commit/86347fd22616d94351827e7058ce3ab5b086f769))
* apply token efficiency to all MCP tools and update descriptions ([df4c962](https://github.com/sthadka/jai/commit/df4c9622e2f10072383e5969fe4641cf271c06d1))
* extend MCP toolset system with full toolset map and env var overrides ([681bcf1](https://github.com/sthadka/jai/commit/681bcf193c1714838bb01619368519a4ac444ad7))
* implement MCP config tool for managing jai configuration ([260aab5](https://github.com/sthadka/jai/commit/260aab58600b5917ca09e4321b99d9d4b568b83b))
* implement MCP prompt templates ([f615c64](https://github.com/sthadka/jai/commit/f615c648944cd00bfb55d402c1f23e5da43957ad))
* implement MCP read and schema tools ([cb93e6f](https://github.com/sthadka/jai/commit/cb93e6fe4850af585a1d070198f5defa4acb48c8))
* implement MCP resource registration ([10690ed](https://github.com/sthadka/jai/commit/10690ed7b97101ab2f6d2c78394bc70ccd9ce09e))
* implement MCP write and sync tools ([83bbb3a](https://github.com/sthadka/jai/commit/83bbb3a3e7829ef9b528c5492340f8b2861b0182))


### Bug Fixes

* changelog sync backfill finding zero candidates ([ccf3da3](https://github.com/sthadka/jai/commit/ccf3da3a3949458872088c9f4f2c568f87f066d4))
* honor --format and --fields in jai get ([85fbfe2](https://github.com/sthadka/jai/commit/85fbfe2a64cdc328e5b1a2fc24844da1fad1aed3))
* make field discovery non-fatal in serve command ([4cd52bc](https://github.com/sthadka/jai/commit/4cd52bcc75b866972f199ac74c70cc50573107c3))
* resolve golangci-lint staticcheck/unused findings ([32222d7](https://github.com/sthadka/jai/commit/32222d727543c5e78c7dece3a34b365954dd676d))


### Refactoring

* restructure Claude Code skill into directory with progressive disclosure ([33180b5](https://github.com/sthadka/jai/commit/33180b51dc13a34fe36f17b10c0fe509e8f5cc77))


### Documentation

* add MCP token efficiency and composability recommendations ([26bf7ae](https://github.com/sthadka/jai/commit/26bf7aecba02fcfe9b2aa262dc062bef0d8cab59))
* add multiple Jira instances section to README ([8a83231](https://github.com/sthadka/jai/commit/8a8323174288f87ad3da41ad6608d25fee1db08a))
* enhance HTTP/SSE transport method documentation ([6336e2c](https://github.com/sthadka/jai/commit/6336e2c1b101534bbf88365b44235fc144c9ee3f))
* update README and user guide for agent integration features ([1dbc890](https://github.com/sthadka/jai/commit/1dbc8904688bb923e1b406bc887aec0e56a795c8))

## [3.1.0](https://github.com/sthadka/jai/compare/v3.0.0...v3.1.0) (2026-08-25)


### Features

* add --raw flag to dump full Jira API JSON ([3906b14](https://github.com/sthadka/jai/commit/3906b14c82daa63e8227cda31ce022c68dba1dd0))


### Bug Fixes

* error out when unauthenticated instead of swallowing the failure ([e5c47cf](https://github.com/sthadka/jai/commit/e5c47cfeda2d14e524d981ac7fe6b58e1be1f320))


### Documentation

* rewrite CLAUDE.md for effectiveness ([3a82a1c](https://github.com/sthadka/jai/commit/3a82a1c7776d414c4dc5989e228788e61899a09f))

## [3.0.0](https://github.com/sthadka/jai/compare/v2.3.0...v3.0.0) (2026-08-03)


### ⚠ BREAKING CHANGES

* `jai set` and `jai comment` now push changes to Jira immediately instead of queuing them locally. `jai transition` no longer side-pushes all queued changes. Use `--queue` / `-q` to restore the old queue-first behavior.

### Features

* write-through to Jira by default for set, comment, transition ([6e871b8](https://github.com/sthadka/jai/commit/6e871b83a29560357f3a66fcdd8a7c212808e4c5))


### Bug Fixes

* suppress repeated field collision warnings on subsequent syncs ([e616051](https://github.com/sthadka/jai/commit/e6160519d1c091528dfba1d8884eb8c55ba3028a))


### Documentation

* add SSH-to-HTTPS fallback for git push in CLAUDE.md ([707cb10](https://github.com/sthadka/jai/commit/707cb10ed1342483e40b4fc07dd558b2f6057d9d))
* update write path documentation for write-through default ([f6e2b76](https://github.com/sthadka/jai/commit/f6e2b767393ea9ff52b9ae417dfe8671803e7cb6))

## [2.3.0](https://github.com/sthadka/jai/compare/v2.2.0...v2.3.0) (2026-07-23)


### Features

* add built-in and parameterized template variables ([e842b69](https://github.com/sthadka/jai/commit/e842b6968cea791ed0e4cba61785e37186cc4ff4))
* add jai clone command for issue cloning with overrides ([3bc0efe](https://github.com/sthadka/jai/commit/3bc0efee9bf9831669318796bb4a18bda979adfd))
* add open command to open issues in browser or print URL ([c6a3e23](https://github.com/sthadka/jai/commit/c6a3e239aa50943fce1c84dbf23c5becd24f00e9))
* add shell completions command for bash, zsh, fish, and powershell ([55f4035](https://github.com/sthadka/jai/commit/55f4035e92e0f31e06cb60e11fdc1e30f6b0d322))
* add user-defined SQL snippets with recursive expansion ([bc334d4](https://github.com/sthadka/jai/commit/bc334d4e4ae153fbd39d65ac8a53aa883ac8b1c6))
* add watch/unwatch commands and remote link support in jira client ([f058468](https://github.com/sthadka/jai/commit/f0584685ad9a4faefdb9148b93c00f84ebe5515a))
* extend link command to support remote URL links ([a7709c1](https://github.com/sthadka/jai/commit/a7709c16cafbe6b85aca0da81af55233d61ec334))


### Bug Fixes

* correct jai set value serialization and bulk local-update gaps ([f3ba1bd](https://github.com/sthadka/jai/commit/f3ba1bd1e7388209ff40eaa91897f485fa5e9552))
* derive {{projects}} template variable from jql-based sync sources ([31ba89d](https://github.com/sthadka/jai/commit/31ba89d8b581167f62cfdcb9f7d20e1e505e4de0))
* refresh local DB after jai transition ([fc84f2f](https://github.com/sthadka/jai/commit/fc84f2f21bc031b3b2ac4e35a7147a526ec9cf2b))
* resolve assignee email to account ID in create and clone ([20d9b72](https://github.com/sthadka/jai/commit/20d9b72c6f41bdcf9bd957d83ed9bbec0c6621c1))
* resolve email to account ID before adding/removing watchers ([9137f6f](https://github.com/sthadka/jai/commit/9137f6fb1099fda8f0e8518756ef847486eea637))
* resolve staticcheck SA5011 lint warnings in test files ([ed49a7d](https://github.com/sthadka/jai/commit/ed49a7d3d46ef323b1dff959248b8e8b912ec0cb))
* skip Jira Rank field when cloning issues ([8eb7a7d](https://github.com/sthadka/jai/commit/8eb7a7dc35216a8c3688fcbbb25fb7b0c049f222))


### Documentation

* add jira-cli assessment and feature adoption recommendations ([0df3abd](https://github.com/sthadka/jai/commit/0df3abdda68815be277b8a3c40fd08eec347fb1b))
* update README and user guide with new commands and features ([62566b0](https://github.com/sthadka/jai/commit/62566b02c0c27ba8c83d4718b910a2188f7d9a29))

## [2.2.0](https://github.com/sthadka/jai/compare/v2.1.0...v2.2.0) (2026-07-22)


### Features

* add --add and --remove flags to jai set for array fields ([38afeef](https://github.com/sthadka/jai/commit/38afeef8e8cd74c40e1b15edabf082d3d620a6fe))
* add bulk set with --query flag and comma-separated keys ([c46323b](https://github.com/sthadka/jai/commit/c46323b9c69134356390581f96068dd4fcaf6c40))
* add jai db command group (reset, path, info) ([e832e32](https://github.com/sthadka/jai/commit/e832e328d12cd5da918e45b2c04f6a21458e8a0d))
* add jai link command for issue link creation ([9005231](https://github.com/sthadka/jai/commit/90052311b857269a0637a9bd6ceece8426343125))
* add jai transition CLI command ([8882a51](https://github.com/sthadka/jai/commit/8882a51306a9914f04923ca72d2b2462dc002e38))
* incremental changelog sync ([894e628](https://github.com/sthadka/jai/commit/894e6281b0687a1de633d7de0f2838faaf49b90c))


### Bug Fixes

* correct set help text, link type resolution, and empty array display ([15de7cb](https://github.com/sthadka/jai/commit/15de7cb1827f8d2bfd5ae3b1d0fa849ddded0b2f))
* resolve all golangci-lint issues ([7e7c21a](https://github.com/sthadka/jai/commit/7e7c21a56277efe1bfe6ec8bb114f0fbeca3d50e))
* wire --config flag through init wizard and expand tilde in db path ([844e10e](https://github.com/sthadka/jai/commit/844e10e0f411009d85a2a4b598bfbfed4aa1291c))


### Documentation

* update README and add user guide for new commands ([27d9f26](https://github.com/sthadka/jai/commit/27d9f2691a3e8c6db782fa1db9303d17a7c59ef9))

## [2.1.0](https://github.com/sthadka/jai/compare/v2.0.0...v2.1.0) (2026-07-22)


### Features

* bulk changelog sync via POST /changelog/bulkfetch ([262d059](https://github.com/sthadka/jai/commit/262d059c86f32c414396dc4740d834231a383d87))
* sync and store Jira changelog/status transition history ([7bc6f2c](https://github.com/sthadka/jai/commit/7bc6f2c6ed9127f72be0339e7a371a90270d5abb)), closes [#5](https://github.com/sthadka/jai/issues/5)


### Bug Fixes

* address PR review feedback on changelog sync ([a995bc4](https://github.com/sthadka/jai/commit/a995bc40ea4cfe8b75f8e9a9ef0c40c07444dad4))
* **ci:** upgrade golangci-lint to v2.12.2 for Go 1.25+ support ([fd2051e](https://github.com/sthadka/jai/commit/fd2051eed6664b1fd737ef8d9fbe42695eb8a7a2))
* handle pre-existing id column in migration 8 ([f48f330](https://github.com/sthadka/jai/commit/f48f330d7c30688f48e939f3243ed630ec5df0b3))
* load all issue ID mappings to avoid SQL variable limit ([f9e95a7](https://github.com/sthadka/jai/commit/f9e95a774d0b3b2b7050260757e9143bfb92c5b1))
* populate issue numeric ID so bulk changelog sync works ([c8dbba6](https://github.com/sthadka/jai/commit/c8dbba6b20a553e7b932c81bb62a567a5b209aea))
* remove unnecessary nil check before range in changelog sync ([b148542](https://github.com/sthadka/jai/commit/b148542a8da985a24aecac098e15a82ef8eb7690))


### Documentation

* document --changelogs flag and changelog table in README ([121a115](https://github.com/sthadka/jai/commit/121a11551d37bee5dd6b1b2be41cadf5e775eb9f))

## [2.0.0](https://github.com/sthadka/jai/compare/v1.1.0...v2.0.0) (2026-06-09)


### ⚠ BREAKING CHANGES

* store array fields as JSON arrays instead of comma-separated text

### Features

* add --stats flag to jai fields for population counts ([9f7176a](https://github.com/sthadka/jai/commit/9f7176a0ca71ccda1500b3c5d3150c6559ba53b5))
* include jira_name in schema db output ([6fc51f4](https://github.com/sthadka/jai/commit/6fc51f46ecdaa26ecda83485bbafcbf2218423f6))
* output jai get as YAML front matter + markdown document ([5b84fa6](https://github.com/sthadka/jai/commit/5b84fa6ba4306c078831edc6057452306b9996f4))
* render description and comment bodies as markdown in jai get ([37cc218](https://github.com/sthadka/jai/commit/37cc2184954d39a7f46739854ba0e8715520f740))
* show jira_name column in jai fields human output ([b660403](https://github.com/sthadka/jai/commit/b66040368c3e1f2e6d36ae7c4dbf0d9f5c91ba13))
* store array fields as JSON arrays instead of comma-separated text ([5f5baca](https://github.com/sthadka/jai/commit/5f5baca7b08f149e29a159c9115ac5166678c533))
* warn on field name collisions during sync ([33125fd](https://github.com/sthadka/jai/commit/33125fdbf2514c66631f54af3dde13d86870d7d1))


### Bug Fixes

* auto-rebuild FTS index when out of sync in jai search ([8c1fdbe](https://github.com/sthadka/jai/commit/8c1fdbe85fd4604793522e29e47fb329722e1570))
* drop FTS triggers during v6 array migration to prevent hang ([08bb0ef](https://github.com/sthadka/jai/commit/08bb0efd16cc2ad74ceea5c68526823e72f6a70d))
* skip unchanged issues during incremental sync ([e2e727f](https://github.com/sthadka/jai/commit/e2e727fdf02829fd1544db4af601b79758996681))

## [1.1.0](https://github.com/sthadka/jai/compare/v1.0.1...v1.1.0) (2026-04-23)


### Features

* add --comments flag to jai get ([9dad24e](https://github.com/sthadka/jai/commit/9dad24e3e56a45126768f0bad63eb72d35a5f9d0))
* add jai create command for creating Jira issues ([ae6b804](https://github.com/sthadka/jai/commit/ae6b804211ff49fb89b46b6fdef457f2bed387e8))
* fall back to Jira API in jai get when issue not in local DB ([483006f](https://github.com/sthadka/jai/commit/483006fc1385672b7c45369c5b788ddaedac3fa4))


### Bug Fixes

* apply --fields filter to human text output in jai get ([0056f02](https://github.com/sthadka/jai/commit/0056f0223e78dc1d46f0d75b91c5234499d3305c))
* handle Jira Team field objects in text-type denormalization ([3a9f772](https://github.com/sthadka/jai/commit/3a9f772e2f3c6e665dcf4cc53a863323abbc4023))
* strip seconds from JQL datetime in cursorToJQL ([1db77f5](https://github.com/sthadka/jai/commit/1db77f562115d2f0c93184358812cd62bbf7b13d))
* warn and prompt when existing token is an unresolved env var in jai init ([99a793a](https://github.com/sthadka/jai/commit/99a793a179444c09684b48cc8252284804c75b63))


### Refactoring

* generic object fallback in field value extraction ([6d5409b](https://github.com/sthadka/jai/commit/6d5409b59cf7cef797b280bf69fa23acd448cdc2))

## [1.0.1](https://github.com/sthadka/jai/compare/v1.0.0...v1.0.1) (2026-04-08)


### Bug Fixes

* repair jai status and incremental sync ([f18f702](https://github.com/sthadka/jai/commit/f18f702075d3fc1b61f93fae259ae99cc444629c))

## 1.0.0 (2026-03-25)


### Features

* add --jql flag to jai query for live Jira queries ([436e703](https://github.com/sthadka/jai/commit/436e7037811bc2a45a07f91a34ac821ea4a27cfe))
* add --resume flag to jai sync --full ([cd3ad6b](https://github.com/sthadka/jai/commit/cd3ad6b317862784c44c20d3e53c56e04a9250ae))
* add issue_links table, resolution, due_date, time tracking, subtask_keys ([8da4519](https://github.com/sthadka/jai/commit/8da451966dd02e172ceeaa8c07a966a6cf9405c4))
* deprecate jira.projects, add jai schema db + schema values ([ede9cde](https://github.com/sthadka/jai/commit/ede9cde6ece1a31093561bd3a70542dcb27d07a8))
* implement group_by rendering in TUI table + plan view ([0069460](https://github.com/sthadka/jai/commit/0069460303dcfdf72e7d88d791c6c201b336e332))
* implement Phase 1 foundation — sync, query, get core loop ([c4b6270](https://github.com/sthadka/jai/commit/c4b627097d73f0a12a6d0dba15406f0e99d9788b))
* implement Phase 2 (agent mode) + Phase 4 (write path) ([d0bfece](https://github.com/sthadka/jai/commit/d0bfecef4b8dbc39ed654dfc90e4b1f36cd5a8b5))
* implement Phase 3 (TUI) and Phase 5 polish ([067f86a](https://github.com/sthadka/jai/commit/067f86a5aa3ca2e7a9df7488307d521c4b932373))
* implement Phase 5 polish — init wizard, deletion detection, color rules, tests ([5747782](https://github.com/sthadka/jai/commit/574778247a47b7b4917d9feaa8b28ca5fb60206c))
* implement Phase 6 — goreleaser, CI/CD, changelog sync ([59db59f](https://github.com/sthadka/jai/commit/59db59f181aa76961d4f1a97c230e8d3db205adc))
* polish init wizard, improve Makefile, fix sync denormalization bug ([a94c3ec](https://github.com/sthadka/jai/commit/a94c3ec7ee16aefa1efb9f3847cf0a8dd7d0fce7))
* rich TUI detail view, field editor, hierarchy config, ADF renderer ([ed4e9da](https://github.com/sthadka/jai/commit/ed4e9da4268d78f8e00eec6ed8f8ec9f19444b6f))


### Bug Fixes

* align Go version to 1.24 to fix golangci-lint CI failure ([90ab26c](https://github.com/sthadka/jai/commit/90ab26ce7d6d8f45bb57b7c871b1913ec20d2c9c))
* continuous spinner and delta-based rate in sync progress display ([e259720](https://github.com/sthadka/jai/commit/e259720ba5c4ed8bbe5abd464b6f93eb511c1230))
* expand summary column to fill available terminal width in TUI ([b81c3e7](https://github.com/sthadka/jai/commit/b81c3e781455270972837ec6a485c237f70a2746))
* field picker and value modals now accept keyboard input + value suggestions ([8cb77c2](https://github.com/sthadka/jai/commit/8cb77c2cbfd3889301d0cecaf4cb251883e27be4))
* group_by viewport and spurious header bugs ([e3baa1b](https://github.com/sthadka/jai/commit/e3baa1b4d2a9019714418df6d232231f4c47b906))
* lower go.mod directive to 1.24.0 to unblock golangci-lint (CI toolchain stays 1.26.1) ([76d633a](https://github.com/sthadka/jai/commit/76d633affe15d648abcf11d214728dc6e53c0262))
* nicer auto-sync spinner and rename Projects to Sources in status ([26a0319](https://github.com/sthadka/jai/commit/26a031925818b4d44ded0e296c0307ad6969eb7e))
* normalize Jira dates to RFC3339 to prevent zero-time display ([068f299](https://github.com/sthadka/jai/commit/068f299d31acc5199effb9190db774f39958368c))
* prevent slice aliasing bug causing duplicate rows in TUI filter ([ee0eff5](https://github.com/sthadka/jai/commit/ee0eff588cde11e5102fe1b27901987906b1efae))
* qualify FTS5 rank column to resolve ambiguity in search JOIN ([d226d26](https://github.com/sthadka/jai/commit/d226d26efcadfee7c8b8d980b195a1153113f387))
* resolve comment dates showing 'Jan 01, 0001' ([5555cb0](https://github.com/sthadka/jai/commit/5555cb0e87cbe87010c3bf358380940190c4b091))
* resolve jai init failures and add named sync sources ([d27748e](https://github.com/sthadka/jai/commit/d27748e247c6856ff5073f2ef10199be305ae854))
* restrict jai query to SELECT/WITH statements only ([308a5e5](https://github.com/sthadka/jai/commit/308a5e5626b97badef24c15052d9452e69e3d23b))
* run go mod tidy to align go.mod with 1.26.1 toolchain (resolves build failure) ([33db1c5](https://github.com/sthadka/jai/commit/33db1c54bf1146f1db451600a5089ef2f3b923cc))
* status command shows correct issue count and pending changes ([4eb33aa](https://github.com/sthadka/jai/commit/4eb33aaf9e5a87acd3043c015343b7615415df78))
* text selection, comment dates, and live field value suggestions ([2132352](https://github.com/sthadka/jai/commit/21323529d015b3b2e20567838141a27455c47903))


### Documentation

* Add init docs ([3600cdc](https://github.com/sthadka/jai/commit/3600cdc3f3dbae90143b57a6d7f9a5fb81d7f4e7))
* update README and config.example.yaml — sync_sources replaces jira.projects, fix brew tap owner ([9807c7c](https://github.com/sthadka/jai/commit/9807c7cd0cf047c02b404a0c00adaa2d93ebb9cb))
