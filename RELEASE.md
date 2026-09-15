# Releasing Spread Plus

Spread Plus releases use a date-based version in `YYYY.MM.DD` format. Publishing
a GitHub Release triggers `.github/workflows/release.yaml`, which builds Linux
archives for amd64 and arm64 and attaches them to the release with checksums.

## Prerequisites

- Write access to the `canonical/spread-plus` repository.
- Permission to publish GitHub Releases and run GitHub Actions.
- Go 1.24 or newer installed locally.
- The GitHub CLI (`gh`) installed and authenticated, when using the commands
  below.

## Prepare the release

1. Choose the release version:

   ```shell
   VERSION="$(date -u +%Y.%m.%d)"
   ```

2. Create a branch from the latest `main`:

   ```shell
   git switch main
   git pull --ff-only origin main
   git switch -c "release-${VERSION}"
   ```

3. Set `version` in `snapcraft.yaml` to `${VERSION}`.

4. Run the local checks:

   ```shell
   go test ./...
   go build -ldflags "-X main.version=${VERSION}" -o spread-plus ./cmd/spread
   ./spread-plus --version
   rm spread-plus
   ```

   The version command must print `${VERSION}`.

5. Commit the version update, push the branch, and open a pull request:

   ```shell
   git add snapcraft.yaml
   git commit -m "Prepare ${VERSION} release"
   git push -u origin "release-${VERSION}"
   gh pr create --fill
   ```

6. Wait for all required GitHub Actions checks to pass, review the generated
   changes, and merge the pull request.

## Publish the release

1. Update the local `main` branch and confirm that the release commit is
   present:

   ```shell
   git switch main
   git pull --ff-only origin main
   git status --short
   ```

   The working tree should be clean.

2. Confirm that the version has not already been released:

   ```shell
   git ls-remote --exit-code --tags origin "refs/tags/${VERSION}"
   ```

   This command should return no matching tag and exit with status 2.

3. Publish the GitHub Release:

   ```shell
   gh release create "${VERSION}" \
       --target main \
       --title "Spread Plus ${VERSION}" \
       --generate-notes
   ```

   Publishing the release creates the tag and starts the `Release spread-plus`
   workflow. Do not create or upload the binary archives manually.

## Verify the release

1. Confirm that the release workflow completed successfully in GitHub Actions.

2. Confirm that the release contains all three generated assets:

   - `spread-plus-amd64.tar.gz`
   - `spread-plus-arm64.tar.gz`
   - `SHA256SUMS`

3. Download and verify the assets:

   ```shell
   mkdir "spread-plus-${VERSION}"
   cd "spread-plus-${VERSION}"
   gh release download "${VERSION}" --repo canonical/spread-plus
   sha256sum --check SHA256SUMS
   ```

4. Extract the archive for the local architecture and check its version:

   ```shell
   tar -xzf "spread-plus-$(dpkg --print-architecture).tar.gz"
   ./spread-plus --version
   ```

   The command must print the release tag exactly.

## Failed releases

If the release workflow fails because of a transient issue, rerun the failed
jobs in GitHub Actions. If the source itself is faulty, fix it through another
pull request and publish a new version. Do not move or reuse a published release
tag.

The GitHub workflow only publishes release archives. Publishing the snap is a
separate process and is not performed by this release workflow.