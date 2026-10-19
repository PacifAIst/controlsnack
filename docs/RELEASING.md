# Making a release

A release is five archives and a checksum file, built by GitHub Actions from a tag. Nothing is built by hand and nothing is uploaded by hand.

1. **The commit is on `main` and CI passed for it.** The workflow refuses a tag that is not on `main`, or whose commit has no successful CI run, so tag only after the checks on GitHub are green.
2. **Tag it and push the tag**, with a version that starts with `v` and has three numbers; a hyphen makes it a pre-release:

   ```
   git tag -a v0.1.0-alpha.1 -m "ControlSnack v0.1.0-alpha.1"
   git push origin v0.1.0-alpha.1
   ```

   Push the tag; do not use the "Draft a new release" button of GitHub, because the workflow creates the release itself and refuses to make a second one for the same tag.
3. **Watch the run** (Actions, "release"). It checks the tag, runs `go run ./tools/dev release`, checks the five archives and `SHA256SUMS` (and that the program says it is the tag), and creates the release with the archives, the checksums and the notes of `.github/release-notes.md`.
4. **If it fails before it publishes**, fix the cause, delete the tag (`git push origin :refs/tags/v0.1.0-alpha.1`, `git tag -d v0.1.0-alpha.1`) and tag again. If a release was published and is wrong, delete the release and the tag on GitHub the same way.

**A trial run** (Actions, "release", "Run workflow" on `main`, or `gh workflow run release.yml`) builds and checks everything and publishes nothing; the archives are kept for a week as an artifact of the run.

**By hand**, to see what a release would contain:

```
go run ./tools/dev release -o dist/release
cd dist/release && sha256sum -c SHA256SUMS
```

The archives are named `controlsnack_<version>_<os>_<arch>` (`.zip` for Windows, `.tar.gz` for the rest); the version is `git describe --tags`, so an archive made from a tag carries the tag. They are not signed (the notes say so), and the same commit gives the same files.
