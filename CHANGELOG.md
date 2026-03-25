# Changelog

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
