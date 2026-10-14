# Releasing

For maintainers. Pushing a `v*` tag publishes a release; contributors do not need this.

1. Update `main` and confirm CI is green: `git switch main && git pull --ff-only`
2. Tag the current `main` commit and push the tag:

   ```bash
   git tag -a v0.2.0 -m "Quark 0.2.0"
   git push origin v0.2.0
   ```

3. The `release` workflow builds `quark-mount` for linux/amd64 and arm64 and creates a GitHub
   release with generated notes, one tarball per architecture and `SHA256SUMS`.

Notes:

- The workflow refuses tags whose commit is not on `main`. Tag after pulling, never a branch commit:
  a squash merge creates a new commit on `main`.
- A `-` in the tag (`v0.2.0-rc1`) marks the release as a pre-release.
- Release tags cannot be moved or deleted (ruleset); only the repository admin can bypass it.
- The GTK app is not part of releases yet.
