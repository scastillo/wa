# wa

A Claude Code plugin that reads WhatsApp Desktop data on a Mac.

- It is read-only.
- It reads, searches and watches the chats you allow.
- It saves the attachments of a chat.

## What you need.

- A Mac (Apple silicon or Intel) with WhatsApp Desktop, linked to your phone.
- Claude Code.
- The GitHub CLI, logged in with read access to this private repo: `gh auth login`.

## Install.

1. In Claude Code, add the marketplace: `/plugin marketplace add scastillo/wa`.
2. Install the plugin: `/plugin install wa@wa`.
3. Restart Claude Code.
4. Ask Claude to check WhatsApp health, or run `/wa:whatsapp doctor`.
   - The first `wa` call downloads the binary for the plugin version.
   - It checks the binary against the release `SHA256SUMS` before it runs it.

## Allow a chat.

Claude reads no chat until you allow it. Run this in your own terminal app, not in Claude Code:

```sh
~/.local/share/wa/bin/wa allow --match "part of the chat name"
```

- `wa allow` refuses to run without a terminal, so chat names never reach the AI session.
- The link `~/.local/share/wa/bin/wa` exists after the first `wa` call in Claude Code.
- The allowlist is `~/.config/wa/policy.json`. Remove a chat with `wa disallow <chat>`.
- macOS can ask your terminal for access to other apps' data. Allow it.

## Use.

Ask Claude in plain words. Examples:

- "What did the book club group say since Monday?"
- "Find the WhatsApp message about the contract."
- "Save all photos from the book club group."
- "Tell me when a new message arrives in the book club group."

The skill runs these commands:

| Command | What it does |
|---|---|
| `wa doctor` | Checks the app, the data, the schema and the allowlist. |
| `wa chats --match T` | Lists allowed chats. Other chats appear only as a count. |
| `wa read <chat> [--since D]` | Prints the messages of an allowed chat. |
| `wa search <text> [--chat C]` | Searches allowed chats. |
| `wa media <chat> [--dry-run] [--dest DIR]` | Saves attachments. The default folder is `~/Downloads/whatsapp`. |
| `wa watch --state F [--seed] [--wait S]` | Waits for the first new message in an allowed chat. |

Exit codes: 0 ok, 1 error, 2 more than one chat matches, 3 the chat is not on the allowlist.

## Privacy.

- `wa` opens WhatsApp's databases read-only. It never writes to WhatsApp's folder.
- The allowlist fails closed. A missing file allows no chat. A file that `wa` cannot parse stops `wa`.
- For chats not on the allowlist, `wa` prints counts only. `wa media` names their files `<date>_<number>.<ext>`.
- Text that `wa` prints goes into the Claude session, so it goes to Anthropic. The allowlist is the control.
- `wa` does not link a device and does not log in. `wa media` fetches only from CDN links that the app already stored, and checks the MAC of each file.

## Limits.

- `wa` sees only what WhatsApp Desktop on this Mac has synced. The app must run for new messages to arrive.
- `wa` cannot get files that only the phone still has.
- A WhatsApp Desktop update can change the data. `wa doctor` then names the missing columns.
- `wa watch` does not report edited or deleted messages.

## Develop.

- Test: `GOTOOLCHAIN=auto go test ./...`. The module needs Go 1.27.1, and `GOTOOLCHAIN=auto` fetches it.
- Local build: `GOTOOLCHAIN=auto go build -o dist/wa ./cmd/wa`. The launcher runs `dist/wa` before the release binary.
- Try the plugin from a checkout: `claude --plugin-dir .`.
- `WA_BIN=/path/to/wa` makes the launcher run that binary.
- Release:
  1. Set the new version in `.claude-plugin/plugin.json` and `.claude-plugin/marketplace.json`.
  2. Add a `CHANGELOG.md` entry for it.
  3. Commit and push `main`.
  4. Run `scripts/release.sh --publish`. It tests, builds both binaries, writes `SHA256SUMS`, and creates the GitHub release `v<version>`.
