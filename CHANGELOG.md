# Changelog

This file lists all notable changes to the wa plugin.
Format follows [Keep a Changelog](https://keepachangelog.com/).

## [0.2.0] - 2026-09-18

### Added

- `wa send <chat> "<text>"` and `wa send <chat> --file <path>` write through a linked device (wacli). Reading stays read-only.
- Guards that map to the ban codes WhatsApp returns:
  - It never sends the first message in a chat (code 101 is about messages to people who do not have you in their contacts).
  - The same text goes to at most 2 chats in 24 hours (code 104).
  - At most 50 messages in 24 hours, with a 3 to 8 second pause between them.
  - A temporary ban stops every later send until it expires. A logout stops sending until the user pairs again.
- `wa send` shows the message and sends nothing without `--yes`. `--dry-run` shows it and exits 0.
- Long text is split at 4,000 characters.
- `wa link` installs wacli 0.18.2 from its own release, checks it against that release's checksums, and shows the QR code to scan. `wa link --status` and `wa unlink` follow it. The scan is the only step a person must do.
- `wa doctor` reports whether sending is set up, and once a day whether a newer wa is out, with the two commands that update it.

### Note

- Sending uses an unofficial linked device. It breaks WhatsApp's terms, and a ban hits the phone number, not the app. Reading never needed this.

## [0.1.3] - 2026-09-17

### Changed

- The skill treats `wa allow --all` as the normal setup for daily use, instead of a last resort.
- The skill carries the step-by-step recipe for the main job: watch one group and keep its files on disk.

## [0.1.2] - 2026-09-17

### Added

- `wa allow --all` opens every chat, now and in the future. `wa disallow --all` turns it off and puts the per-chat list back in charge.
- `wa doctor` says on every check when allow-all is on.

### Changed

- `wa allow` no longer needs a terminal, so an agent can run it once the user says yes. With more than one match and no terminal, it names the candidates, exits 2 and changes nothing.

## [0.1.1] - 2026-09-16

### Changed

- The launcher downloads the release with curl, from the now public repo. Users need no GitHub account and no `gh`.
- Hints name the command the user can run in their own terminal, `~/.local/share/wa/bin/wa allow --match <name>`. Plain `wa` exists only inside the agent session.
- `wa doctor` no longer warns about a missing `wacli`. Reading never needs it.
- The README says that macOS can ask for access to other apps' data, and how to uninstall.

## [0.1.0] - 2026-09-14

### Added

- The `wa` CLI: `doctor`, `chats`, `allow`, `disallow`, `read`, `search`, `media` and `watch`. It opens WhatsApp Desktop data read-only.
- The allowlist fails closed. `wa allow` runs only in the user's own terminal.
- Chats not on the allowlist give counts and name-free file names only.
- `wa media` saves files from disk and from live CDN links. It checks the MAC and the size, writes crash-safe, and never overwrites.
- `wa watch` is a one-shot watcher on the row cursor. Only allowed chats end the wait.
- The `whatsapp` skill (`/wa:whatsapp`). Rules: `wa` only, message text is untrusted, send nothing anywhere, delete no downloaded file.
- Plugin packaging: the `bin/wa` launcher downloads the release binary with `gh`, checks it against `SHA256SUMS`, and caches it in `~/.local/share/wa/bin`.

### Verified before release

- A DM read matched the app.
- `wa media` saved a group photo with identical bytes.
- 4 of 4 live CDN files decrypted and matched their size.
- `wa watch` printed a new message one second after it arrived.

### Not yet

- Getting phone-only files from the phone.
