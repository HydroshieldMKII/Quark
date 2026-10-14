# Troubleshooting

**`bwrap: Can't find source path /run/user/.../doc/by-app/...` when starting the app**

The XDG document portal lost its FUSE mount. Run `systemctl --user restart xdg-document-portal`.

**`quark-mount`: folder is busy on unmount**

Close programs that have files open in the folder, then `fusermount3 -u ~/ProtonDrive`.

**`quark-mount` exits with `not signed in`**

Run `proton-drive auth login`, then `systemctl --user start quark-mount`. The unit does not retry
this failure (exit code 78); `make install-mount` updates an older unit file.
