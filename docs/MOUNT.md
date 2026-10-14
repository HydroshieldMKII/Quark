# quark-mount

Mounts Proton Drive as a folder (`~/ProtonDrive`) for Files and any other program.

## Install

From a [release](https://github.com/HydroshieldMKII/Quark/releases) (see `INSTALL.txt` in the tarball) or from a clone:

```bash
make install-mount
systemctl --user enable --now quark-mount
nautilus -q
```

Requires `fuse3` and a signed-in `proton-drive` CLI (`proton-drive auth login`).

## Behaviour

- **Open**: files download on first open into a bounded cache (1 GiB default, oldest evicted first).
  Folder listings are cached for offline browsing.
- **Save**: the change is journaled on disk, then uploaded as a new version, with retries when
  offline. If the remote file changed meanwhile, the upload becomes `name (conflict YYYY-MM-DD HH-MM).ext`.
- **Keep on this device**: Files context menu or `quark-mount pin PATH`. Pinned items are kept
  current and never evicted. **Free up space** drops the local copy.
- **Delete** moves to the trash.
- Names with characters invalid on Linux (`: * ? " < > |`) appear with `_` and are read-only.

| Emblem | State |
|---|---|
| green check | on this device |
| grey check | cached |
| blue cloud | online only |
| orange arrow | transferring or pending upload |
| red | upload failed, will retry |

## Commands

```
quark-mount status MOUNTPOINT        pending changes, pins, cache use
quark-mount sync MOUNTPOINT          retry pending uploads now
quark-mount pin|unpin|evict|state PATH...
```

State and controls are also exposed as extended attributes (`user.quark.state`, `user.quark.pin`).

## Limits

- Creating a new file requires a connection; editing an existing one does not.
- Runs on the host; FUSE is unavailable inside the Flatpak sandbox.
- Copies that could not be journaled are kept in `~/.cache/quark/mount/unsynced`.
