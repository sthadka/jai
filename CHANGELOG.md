# Changelog

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
