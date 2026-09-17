# Changelog

This file lists all notable changes to the wa plugin.
Format follows [Keep a Changelog](https://keepachangelog.com/).

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
