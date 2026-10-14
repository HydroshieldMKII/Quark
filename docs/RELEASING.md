# Releasing

Pushing a `v*` tag publishes a release.

1. `git switch main && git pull --ff-only`
2. Tag and push:

   ```bash
   git tag -a v0.2.0 -m "Quark 0.2.0"
   git push origin v0.2.0
   ```

The `release` workflow builds `quark-mount` for linux/amd64 and arm64 and creates a GitHub release
with generated notes, one tarball per architecture and `SHA256SUMS`.

- Tags whose commit is not on `main` are refused.
- A `-` in the tag (`v0.2.0-rc1`) marks a pre-release.
- Release tags are protected against moves and deletion.
