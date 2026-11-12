# Session storage

`App.Build` uses JSONL by default, under `<workspace>/.codes/sessions`.

Each session is one UTF-8 JSONL file. The filename uses a UTC creation timestamp
and a random suffix. There is no session index or next-ID counter, on disk or
in memory. The filename itself is the stable string key used by Store and App.

```jsonl
{"type":"session","created_at":"2026-09-05T08:00:00Z","updated_at":"2026-09-05T08:00:00Z"}
{"type":"message","message":{"Role":"user","Content":"hello","ToolCalls":null,"ToolResult":null}}
{"type":"session_updated","updated_at":"2026-09-05T08:05:00Z"}
```

The first record stores the creation time and initializes `updated_at` to it.
Leaving a session (switching, creating another, clearing it) or gracefully closing
CodeS appends `session_updated`; the latest such record determines `UpdatedAt`.
Reading, listing, resuming the target session, and appending messages do not change
these timestamps. Force-killing the process or losing power cannot guarantee an
exit timestamp. `ListSessions` returns
`CreatedAt` and `UpdatedAt` and sorts by most recently modified first. Message
contents (including tool calls/results) use the existing `Chat.Message` format.

Use `/new` to start a session and `/resume` to select a previous conversation.
The UI does not display session file names. Existing JSONL files remain usable
without migration.

New sessions initially have a metadata-only file. When leaving a session or
closing CodeS normally, App loads its stored messages: if there are none, it
deletes that session file instead of appending an exit timestamp. A user message
alone is enough to keep the file, even if the model fails to answer. Creation
metadata and exit records are not chat messages. Previously abandoned empty files
are not bulk-deleted; opening and then leaving one applies the same cleanup.

Writes are appended and synced. A malformed, empty, or incomplete file produces
an explicit error, without silently discarding history or appending to corruption.
Back up the file before manually repairing it. Each append currently validates
the existing file; long transcripts therefore increase write cost.

`Store.TouchSession` records this exit update. `App.Close()` returns a
timestamp write error to its caller; `main` reports it after the TUI exits.

Operations through one store instance are serialized. Concurrent writers from
multiple processes to the same session file are not supported. `DeleteSession`
removes that file; it does not provide a recycle bin.
