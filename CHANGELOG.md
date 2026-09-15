# Changelog

This file lists all notable changes to the wa plugin.
Format follows [Keep a Changelog](https://keepachangelog.com/).

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
