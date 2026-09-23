// MySQL/MariaDB adapter safety and operational notes:
//
//   - TLS accepts empty/true (certificate and hostname verification) or explicit
//     off. preferred and skip-verify are intentionally rejected. For sockets,
//     explicitly select off unless the server supports verified TLS over sockets.
//   - Query opens a server-enforced read-only transaction and always rolls back.
//     A conservative lexical guard also rejects writes, transaction control,
//     INTO, comments, and backslash escapes. It can reject otherwise valid reads.
//     This is NOT a sandbox. Functions can perform external effects, acquire
//     locks, or affect temporary tables; server privileges remain essential.
//     Use a restricted account without FILE, EXECUTE, or administrative grants
//     when querying untrusted SQL. Query/Execute connections are discarded to
//     prevent locks and session settings leaking into later requests.
//   - Execute requires caller-side explicit write authorization. It does not
//     implement confirmation or policy itself. Multi-statements are disabled.
//   - Results retain at most 1000 rows, 1024 columns, 64 KiB source bytes per
//     value, and 8 MiB encoded value text in total. Binary data is hexadecimal;
//     decimals, large integers, and dates remain textual. Truncation is reported
//     in Notice and each affected Value.Truncated flag. Exceeding the total byte
//     budget returns an error with no partial page, even if the first row alone
//     exceeds the budget. HasMore indicates only rows beyond the row limit.
//   - These are retained-result limits, NOT a strict process-memory or server-work
//     bound. The driver receives a whole row/packet before values can be clipped;
//     maxAllowedPacket is not an inbound row-size guarantee. Use server/resource
//     limits for hostile queries or enormous BLOBs. Cancellation stops draining
//     results at the cap; later result sets are not inspected after cancellation.
//     Multiple result sets encountered before the cap are rejected.
//   - Browse uses parameterized server filters and LIMIT/OFFSET, not keyset
//     pagination. Concurrent mutations can shift pages. Primary keys or non-null
//     unique indexes break sort ties; tables without one get a fallback warning.
//     Metadata is capped at 10000 entries and 1024 columns. ForeignKey.Database
//     identifies the referenced schema for both same- and cross-database targets.
//   - Each operation has a 30-second deadline (or the caller's earlier deadline).
//     The pool holds at most four connections with three-minute lifetimes.
//
// Tests run without a server using go test ./internal/database/mysql. Optional
// integration tests use RELVO_TEST_DSN, pointing at an isolated, existing database
// with CREATE/DROP/INSERT privileges. They create and remove one uniquely named
// table. Only endpoint, database, credentials, and supported TLS settings are
// taken from that DSN; arbitrary driver/session options are not imported.
//
// Driver reference: https://github.com/go-sql-driver/mysql/blob/master/README.md
package mysql
