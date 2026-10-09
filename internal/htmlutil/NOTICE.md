`internal/htmlutil` and `internal/markdown` are copied from
[basecamp/hey-cli](https://github.com/basecamp/hey-cli) (MIT, see
THIRD_PARTY_NOTICES.md) at commit fetched 2026-10-01. Changes: import paths
point at mailday's own `internal/terminal`, and the HEY blob-URL check is a
stub, because mail bodies never contain HEY attachment paths.
