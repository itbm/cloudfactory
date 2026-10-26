# Releases and signing keys

## Make the signing key

Do these steps one time, before the first release.

1. Install cosign.
2. Make a key pair:

   ```sh
   cosign generate-key-pair
   ```

3. Add the private key and its password to the repository secrets:
   - `COSIGN_PRIVATE_KEY`: the contents of `cosign.key`.
   - `COSIGN_PASSWORD`: the password of the key.
4. Commit `cosign.pub` to the root of the repository.
5. Keep a copy of `cosign.key` in a safe place offline. Then delete it from your computer.

The release pipeline stops if `cosign.pub` is not in the repository.

**Caution:** If you lose the private key, you cannot sign updates. Servers do not accept
images that have a signature from a different key.

## Make a release

1. Make sure that CI passes on `main`.
2. Make a tag with the version, for example:

   ```sh
   git tag v0.1.0
   git push origin v0.1.0
   ```

The workflow [`release.yml`](../.github/workflows/release.yml) then does these steps:

1. Build each image for x86-64 and ARM64.
2. Scan each image with Trivy. The release stops if Trivy finds a critical
   vulnerability that has a fix.
3. Push each image to `ghcr.io/<owner>` with the version tag.
4. Make an SBOM for each image. Sign each image and attach the SBOM with cosign.
5. Build `cf-agent` and `cf` for x86-64 and ARM64.
6. Put the version, the gVisor version, the registry and the public key into `install.sh`.
7. Sign each file and make a `.sha256` file for each file.
8. Make the GitHub release with all files.

## Daily image rebuild

The workflow [`rebuild.yml`](../.github/workflows/rebuild.yml) runs each day. It checks the
digest of each base image. If a base image changed, it starts `release.yml` with
`images_only=true`. The new images get the tag `<version>-r<run number>`. The panel then
offers the new tag as an update.

## Change the gVisor version

Change `GVISOR_VERSION` in `release.yml` and the default value in
`agent/internal/config/config.go`. Use a release from
<https://gvisor.dev/docs/user_guide/install/>.
