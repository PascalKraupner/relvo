# Relvo

A terminal database browser for MySQL and MariaDB, with a local control socket for tools and AI assistants. Browse tables, inspect schema and row values, apply filters, and run SQL without leaving the terminal. Writes are disabled by default and require local approval when enabled.

## Build and Demo

Requires Go 1.26+ and a Unix-like environment. Windows is not supported. The UI uses Bubble Tea, Bubbles, and Lip Gloss v2; dependency versions are pinned in `go.mod`.

```sh
git clone https://github.com/PascalKraupner/relvo.git
cd relvo
go build -o bin/relvo ./cmd/relvo
./bin/relvo --demo --session demo
```

The demo automatically connects to an in-memory seed containing users, products, and orders. It supports browsing, filtering, sorting, and schema inspection, but not raw SQL queries or writes. No database server is needed.

Examples below assume the built `relvo` binary is on `PATH`.

## Development

Run these commands from the project directory. Install [Just](https://just.systems/) and use `just` to list the available recipes.

```sh
just dev                  # Demo with Air hot reload
just build                # Build bin/relvo
just run                  # Build and launch the connection browser
just run --demo            # Build and launch the demo without a watcher
just check                # Formatting check, vet, race tests, and build
just fmt                  # Format Go files
just test -count=1         # Run tests without cached results
```

`just dev` installs pinned Air v1.67.4 into the ignored `bin/tools/` directory on first use; it does not require a global Air install or change application dependencies. `just tools` installs it explicitly. Go files, `go.mod`, and `go.sum` trigger rebuilds. Test-only edits, `.env`, and generated output do not. Restart `just dev` after editing `.air.toml`.

Arguments replace the default `--demo` and are forwarded safely, including paths and names containing spaces:

```sh
just dev --env '/path/to/my project/.env' --tls off --connect project
just dev --demo --session relvo-dev
```

Air rebuilds and restarts the process, not individual components: temporary connections, filters, drafts, and pending approvals reset. Never use live development against a production database. The development-only launcher in `scripts/dev/` handles foreground terminal ownership so keyboard input works under Air, then restores it on exit. Press `q` (or Ctrl+c while idle) to exit the TUI, then Ctrl+c again to stop the watcher. Build diagnostics are retained in `tmp/build.log` and shown after a failed rebuild closes the TUI; fix the source and Air starts it again.

## Connect

From a Laravel project directory:

```sh
relvo --tls off --session shop
```

Relvo reads only the current directory's `.env`, not the whole project tree or parent directories. It offers a candidate named `<directory> (.env)` for MySQL/MariaDB configuration. Press `c` to choose it, then Enter to connect or Ctrl+t to test. Discovery does not connect automatically. Outside demo mode, startup connects only when `--connect NAME` is supplied:

```sh
relvo --tls off --connect 'shop (.env)' --session shop
```

TLS defaults to `true`, with certificate and hostname verification, including for Unix sockets. Use `--tls off` explicitly for trusted local servers without verified TLS. `preferred` and `skip-verify` are not accepted. An environment file's `DB_TLS` overrides the CLI fallback, so check it if `--tls off` appears ineffective.

For a Compose service hostname such as `DB_HOST=mysql`:

```sh
relvo --docker --tls off --session shop
```

`--docker` inspects the current Compose project's running services and offers an additional candidate for an already-published TCP port matching the configured service name and target port. It does not start containers, publish ports, or change Compose files. Without a published port, use another reachable endpoint.

Docker candidates preserve the original environment-file path, mapping, and password-variable reference. On connection, those credentials are resolved again; an `Endpoint` override is applied last to select the published host/port and clear any Unix socket setting.

### Custom Configuration

Use an explicit file and repeatable mappings for non-Laravel variable names:

```sh
relvo --name staging --env /path/to/staging.env \
  --map host=MYSQL_HOST --map port=MYSQL_PORT \
  --map user=MYSQL_USER --map password=MYSQL_PASSWORD \
  --map database=MYSQL_DATABASE --connect staging
```

Mappable fields are `host`, `port`, `user`, `password`, `database`, `socket`, and `tls`. Unmapped fields use `DB_HOST`, `DB_PORT`, `DB_USERNAME`, `DB_PASSWORD`, `DB_DATABASE`, `DB_SOCKET`, and `DB_TLS`. Explicitly mapped variables must exist in the file. File values override explicit connection fields; `--password-env VARIABLE` overrides the password from the process environment last. Files are reread when resolving a connection and do not change the process environment.

For direct configuration, supply `--host`, `--port` (default `3306`), `--user`, `--database`, and `--password-env`; `--socket` accepts an absolute MySQL Unix socket path instead of TCP. Keep `.env` files private and out of version control. Relvo reads them but does not enforce or change their permissions.

### Profiles and Sessions

Press `n` for a manual connection or `e` for an environment-file mapping form. Tab moves between fields; Ctrl+s connects and adds the profile **in memory only** on success. Ctrl+t tests the draft without adding or saving it. Failed setup preserves fields for correction and does not reserve the profile name or replace the current connection. Neither form edits `.env` or persists profiles; dismissing a form drops its password-bearing fields.

The manual form's TLS field defaults to `true`; enter `off` for a trusted local server without verified TLS. The `e` form's TLS field is a **variable name**, defaulting to `DB_TLS`, not a literal TLS override. Set `DB_TLS=off` in the private file (or map another variable containing `off`), or use CLI setup such as `relvo --env /path/to/.env --tls off`. Startup `--tls off` is not inherited by new `e` form drafts, and file TLS values take precedence over the CLI fallback.

Persist a profile through the CLI instead:

```sh
# SHOP_DB_PASSWORD must already be set in the process environment.
relvo connections add --name local --host 127.0.0.1 --port 3306 \
  --user shop_reader --database shop --password-env SHOP_DB_PASSWORD --tls off
relvo connections list
relvo --connect local --session shop
```

Saved profiles contain connection settings and references, not password values. `PasswordEnv` is serialized as `password_env`; an environment-file profile retains its path and mapping. Profile saves atomically replace a mode-0600 JSON file. The default is the user config directory's `relvo/connections.json` (`$XDG_CONFIG_HOME/relvo/connections.json` when set). Use `--profiles PATH` consistently with startup and `connections list|add` for a different file. `add` validates resolution, not live connectivity; existing names cannot be replaced by that command.

Concurrent `connections add` calls use an interprocess `flock` around load, validation, and save to avoid lost updates. The sibling `<profiles path>.lock` file is retained; do not remove it while writers may be running. Manual JSON edits do not participate in this lock.

`--session NAME` names the live control socket, not a saved database session. The default is `relvo-<pid>`. Selection, filters, page offsets, and pending writes are not persisted across restarts. Use `--no-control` to disable the socket.

## Controls

| Key | Action |
| --- | --- |
| Tab / Shift+Tab | Switch table/results focus; reveal panes in narrow terminals |
| Arrows / hjkl | Move selection |
| Enter | Open selected table or inspect selected row's retained values |
| `/` | Fuzzy table search in sidebar; add AND filter in results |
| `i` / `x` | Filter by first primary-key column / clear filters in results |
| `f` | Follow the selected column's foreign key using the current row |
| `s` | Toggle selected column's ascending/descending sort |
| `[` / `]` | Previous/next database page |
| `+` / `-` | Change page size by 25, within 1..1000 |
| `gg` / `G`, Home / End | First/last loaded row, not database page |
| Ctrl+d / Ctrl+u, PgDown / PgUp | Scroll within loaded data |
| `1` / `2` / `3`, `Q` | Data / Structure / SQL editor; `Q` also opens SQL |
| Ctrl+s / Ctrl+Enter | Submit SQL from the editor |
| `W` / `a` | Stage write SQL / review pending write |
| `c` / `n` / `e` | Connection picker / manual connection / environment mapping |
| `v` / Ctrl+p / `?` | Row/cell highlight / command palette / help |
| `r` | Refresh tables and current table data |
| Esc | Close modal; outside modals cancel busy work and clear table search |
| Ctrl+c / `q` | Ctrl+c cancels while busy, otherwise quits; `q` quits outside inputs |

Mouse support includes table selection, tabs, column-header sorting, cell selection, scrolling, and footer pagination. Row inspectors and write confirmations scroll with PgUp/PgDown or the wheel. Composite primary-key lookup via `i` uses only the first key column; add further filters as needed.

The data grid shares the available terminal width across visible columns; columns beyond the viewport remain reachable with `h`/`l`, arrow keys, and mouse selection. The sidebar and results have separate backgrounds and a visible divider. `?` opens a centered keybind overlay above the current table; press `/` there to search shortcuts, use arrows or PgUp/PgDown to scroll, and Esc to close (or clear an active search first).

Foreign-key navigation via `f` opens a listed target table in the same database with equality filters for every field of the constraint, including composite keys. It requires exact retained row values: NULL, binary, truncated, missing, or ambiguous keys cannot be followed. Cross-schema navigation is not supported.

Query and write editors retain separate drafts in memory when closed or submitted. Reopening an editor restores its draft; drafts are not saved across restarts or cleared merely by closing the modal.

## SQL and Limits

Raw reads use a server-enforced read-only transaction that is always rolled back, plus a conservative lexical guard. The guard permits read statement forms such as SELECT, WITH, SHOW, DESCRIBE, and EXPLAIN, but rejects write/transaction-control tokens, INTO, comments, and backslash escapes. Some otherwise valid reads are rejected. Raw SQL connections are discarded after use; multi-statements are disabled.

**This is not a SQL sandbox.** Stored functions can have external effects, acquire locks, or affect temporary tables. Use a restricted database account without FILE, EXECUTE, or administrative grants for untrusted SQL. Read-only mode and cancellation do not replace database privileges or server resource limits.

MySQL results retain at most 1000 rows, 1024 columns, 64 KiB source bytes per value, and 8 MiB encoded value text overall. Clipped values carry `truncated: true` in JSON and are reported in `notice`. Exceeding the total 8 MiB budget returns an error, not a partial page; reduce page size or select fewer/smaller values. `has_more` indicates rows beyond the row limit only. Large integers, decimals, and dates remain text; NULL is distinct from empty text and binary values are hexadecimal. These are retained-result limits, not strict process-memory or server-work bounds: the driver receives a whole row/packet before clipping. Operations have a 30-second deadline or the caller's earlier deadline.

To enable write staging, start with `--allow-writes` and a suitable database account. `W` or `ctl write` stages SQL without executing it. Review the exact SQL, connection, and database in the local TUI, type `approve`, then Enter within five minutes. Ctrl+r rejects; Esc only closes the review. There is no remote approval command. Approval is consumed before execution; if execution fails or times out, inspect the database before retrying because the outcome may be unknown.

Current boundaries:

- Table pagination uses LIMIT/OFFSET, not keyset pagination. Concurrent changes can shift pages; large offsets can be expensive.
- There is no external database change stream or periodic refresh. Press `r` to reload changes made by other clients; approved writes trigger a refresh.
- Raw query results do not support the table filter/sort/page controls; edit the SQL instead. Refresh returns to table browsing.
- Writes are raw SQL only. There is no structured row insertion, cell editing, or row deletion UI yet.
- Structure displays columns, indexes, and foreign keys, including the referenced database. Navigation is limited to same-schema targets with exact usable row keys.

## Local Tools

```sh
relvo ctl sessions
relvo ctl --session shop state
relvo ctl --session shop open-table users
```

Tools share the same application state as the TUI. Output includes row data and must be treated as sensitive. See [control protocol and commands](docs/control.md) and [architecture](docs/architecture.md). The agent guide is [skills/relvo/SKILL.md](skills/relvo/SKILL.md); it can be installed or copied through an agent's normal skill mechanism. Relvo does not install it or modify agent configuration.

## Development Checks

```sh
go build -o bin/relvo ./cmd/relvo
go vet ./...
go test ./...
go test -race ./...
```

The [GitHub Actions workflow](.github/workflows/ci.yml) runs Go 1.26 build, vet, and race tests on Ubuntu. A separate job runs the adapter integration test against MySQL 8.0 and MariaDB 11.4 containers. Workflow configuration is not evidence of a successful run.

Without `RELVO_TEST_DSN`, the MySQL integration test skips and the remaining tests need no server. To opt in, set that variable privately to a Go MySQL driver DSN for an **isolated, existing** database, then run:

```sh
go test ./internal/database/mysql -run '^TestIntegration$' -count=1
```

The test requires CREATE, DROP, INSERT, and SELECT access. It creates and removes one uniquely named InnoDB table and checks browsing, metadata, value fidelity, multi-statement rejection, and server read-only transaction behavior. Only endpoint, credentials, database, and supported TLS settings are imported from the DSN, not arbitrary driver/session options. In this test harness, omitted/false DSN TLS means off; normal application TLS still defaults to verified.
