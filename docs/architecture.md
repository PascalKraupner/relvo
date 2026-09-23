# Architecture

Relvo is one process with two frontends over a shared application state: a terminal UI and a local Unix-socket control server. Only one database store is active at a time.

```text
cmd/relvo
  +-- connections: saved profiles, explicit config, .env, optional Compose endpoints
  +-- tui ---------+
  +-- control -----+--> app --> database.Store --> mysql or demo
                        |
                        +--> cloned revision snapshots --> tui / control responses
```

## Package Boundaries

| Package | Responsibility |
| --- | --- |
| `cmd/relvo` | Parse startup/profile commands, assemble candidates, choose store opener, start control server and TUI, close resources |
| `internal/connections` | Read profile JSON, resolve credentials, inspect current `.env`, and discover matching published Compose endpoints without connecting |
| `internal/app` | Own connection, schema, browse request, results, pending approval, cancellation, and state transitions |
| `internal/database` | Small `Store` interface and shared configuration, schema, filter, and textual-value types |
| `internal/database/mysql` | MySQL/MariaDB pool, information_schema queries, parameterized browsing, bounded raw reads, and caller-authorized execution |
| `internal/database/demo` | Independent deterministic in-memory ecommerce seed; browsing only, no SQL engine |
| `internal/control` | Private Unix sockets, session discovery, strict JSON-RPC request validation, CLI-to-action mapping |
| `internal/tui` | Bubble Tea v2 model, input forms, viewport/selection, rendering, and local approval key handling |

There is no ORM, background database watcher, or separate agent service. `database.Store` exposes `Tables`, `Schema`, `Browse`, `Query`, `Execute`, and `Close`; authorization belongs to `app`, not `Execute` itself.

## State and Concurrency

`app.App` uses two locks. The operation lock (`op`) permits one state-changing operation in flight. Dispatch, profile addition, and rejection use `TryLock`: competing work is rejected rather than queued against potentially stale state. The state mutex (`mu`) protects snapshots, cancellation, and event publication. `state` and `cancel` bypass the operation lock, so tools can inspect or cancel busy work. This is not optimistic concurrency control: actions carry no expected-revision field.

An operation publishes a busy snapshot, works against a cloned snapshot with a 30-second context deadline, then publishes success or an error with busy cleared. Cancellation is cooperative. Close first cancels, waits for the operation lock, closes the event channel, and closes the active store.

Each publication increments `revision`. Snapshot cloning includes nested result rows, filters, schema/index slices, defaults, and pending writes so receivers do not share mutable state. The event channel has capacity one and replaces an unread event with the newest snapshot. It is a latest-state feed for the TUI, not an audit log or broadcast subscription service. The TUI also receives command results; it rejects any event/result older than its current revision to avoid stale rendering.

Connection forms call `app.Setup`, which tests or adds-and-connects under one operation lock rather than separate add/dispatch calls. Tests never add profiles; failed setup neither reserves a name nor replaces the connection. The TUI keeps failed fields for correction, blocks duplicate submissions, and uses form IDs so late completions cannot close a different form. Query and write drafts are separate TUI-owned strings retained in memory on close/submission, not persisted history.

Connection switching opens and lists the new database before replacing the old store. Browse settings are remembered per connection only in memory. A successful connection remains selected if its first table cannot load. Opening a table loads schema and a bounded page; refreshing reloads the table list and returns to table browsing. External database mutations are visible only on subsequent reads, not through a change stream.

## Database Work

Metadata comes from `information_schema`, scoped by configured schema and table. Identifiers for generated browse SQL are quoted; filter/sort columns are checked against metadata and values are parameters. Up to 100 AND filters are supported. `contains` escapes LIKE wildcards and searches for literal text, with comparison behavior otherwise governed by the server.

Browsing uses LIMIT/OFFSET and requests one extra row to detect another page. Ordering uses the selected column plus a primary key or suitable non-null unique index to break ties. Without such a key it falls back to the first column and warns about nondeterministic ties. Stable tie-breaking does not prevent offset pages shifting under concurrent mutations. There is no keyset implementation or benchmark-backed performance guarantee.

The MySQL pool holds at most four connections, with a three-minute lifetime and one-minute idle timeout. Dial timeout is five seconds; operations and driver reads/writes have 30-second timeouts. Raw queries run in read-only transactions with deferred rollback; query and execution connections are discarded afterward to avoid leaking session settings or locks. Write execution is a single raw statement, not an application-managed preview/rollback transaction.

Results retain text rather than converting large identifiers or decimals to floating-point JSON numbers. NULL, binary, and per-value truncation flags preserve distinctions. Values clipped at 64 KiB carry `truncated: true`; exceeding the 8 MiB total text budget returns an error with no partial page. `HasMore` signals only rows beyond the row limit. These budgets bound retained results, not inbound driver packets or total server work. Metadata is capped at 10,000 entries and 1024 columns. Multiple result sets encountered before a cap are rejected; cancellation at a cap means later result sets are not inspected. See [README safety and limits](../README.md#sql-and-limits) and `internal/database/mysql/doc.go` for the operational caveats.

The TUI implements `f` by finding the selected column's foreign-key constraint, gathering every component from the selected row, and dispatching `open-table` with equality filters on the target columns. Composite constraints are followed together; NULL, binary, truncated, missing, inconsistent, or ambiguous key data is rejected. `ForeignKey.Database` identifies the referenced schema, and navigation rejects cross-schema targets. There is no separate follow-foreign-key app action or RPC.

## Trust and Persistence

Startup discovery reads only the current `.env`; explicit paths are opt-in. Resolution rereads a file without exporting it into the process environment. Docker candidates retain `EnvFile`, `Mapping`, and `PasswordEnv` rather than freezing resolved credentials. Their `Endpoint` override applies last, replacing host/port and clearing the socket while leaving credential and TLS resolution intact.

Saved profiles store references to secrets and omit `Config.Password` from JSON. `connections.Add` holds an exclusive interprocess `flock` on a retained sibling lock file across load, duplicate-name validation, resolution, and save. Saves use a temporary private file, sync, and rename. This prevents lost updates among cooperating additions; separate Load/Save calls or external editors do not share that protection. Manual TUI profiles, SQL drafts, browse state, and write approvals never go to disk through this persistence path.

The control interface exposes snapshots and a limited action set, not credentials, profile creation, or approval. Unix ownership/permissions are the local access boundary, not isolation from other processes running as the same user. The TUI escapes terminal control characters in untrusted text; this does not make database contents safe instructions for an agent.

With writes enabled, `app` stages at most one unexpired pending write, binding a random ID to exact SQL, connection, database, and a five-minute expiry. Local approval checks that binding and consumes it before calling `Execute`, preventing replay even after a timeout. Switching connections clears pending approval. The control dispatcher deliberately lacks `Approve` and `Reject`; only the TUI backend includes them. Read-only SQL protections cannot prevent every stored-function or external side effect, so restricted database grants remain essential.

Structured row editing, keyset pagination, cross-schema foreign-key navigation, persistent query history, external change streaming, and Windows support are not implemented. In-memory SQL drafts are not persistent query history.
