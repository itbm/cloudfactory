# Releases and signing keys

## Make the signing key

Do these steps one time, before the first release. Do them on your own computer. You must
have admin access to the repository.

The release workflow signs with a key pair. The private key is in the repository secrets.
The public key, `cosign.pub`, is in the repository. Each server gets `cosign.pub` when you
install it. Podman, the panel, the agent and the installation script use it to verify the
images and the files of a release.

1. Install [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) and the
   [GitHub CLI](https://cli.github.com/). Sign in to the GitHub CLI with `gh auth login`.
2. Make a key pair. Cosign asks for a password. Use a long random password, and keep it in
   your password manager:

   ```sh
   cosign generate-key-pair
   ```

   This makes `cosign.key` (the private key, encrypted with the password) and `cosign.pub`.
3. Add the private key and its password to the repository secrets. The second command asks
   for the password:

   ```sh
   gh secret set COSIGN_PRIVATE_KEY --repo itbm/cloudfactory < cosign.key
   gh secret set COSIGN_PASSWORD --repo itbm/cloudfactory
   ```

4. Commit `cosign.pub` to the root of the repository in a pull request. Do not commit
   `cosign.key`. The file `.gitignore` excludes it.
5. Keep a copy of `cosign.key` and its password in a safe place offline, for example in your
   password manager. Then delete `cosign.key` from your computer.

The workflow [`signing-check.yml`](../.github/workflows/signing-check.yml) runs on the pull
request that changes `cosign.pub`. It signs a test file with the key in the secrets and
verifies the signature with `cosign.pub`. If it passes, the key is ready. You can also start
it from the Actions tab at any time. It publishes nothing.

The release pipeline stops before it builds anything if `cosign.pub` or a secret is missing.

**Caution:** If you lose the private key, you cannot sign updates. Servers do not accept
images that have a signature from a different key. GitHub does not let you read a secret
after you set it, so the offline copy is the only copy that you can get back.

### Why the release does not use keyless signing

Cosign can also sign with a short-lived certificate from Sigstore, with no private key. We
do not use this. Four parts verify the signatures: the Podman signature policy, the panel,
the agent self-update and the installation script. The installation script uses only
OpenSSL. To verify a keyless signature, each part must check a Fulcio certificate chain, the
identity of the workflow and a Rekor transparency log entry. A fixed public key is easier to
verify correctly in all four parts.

### Change the key

Change the key only if it is lost or exposed. Servers that you installed before the change
keep the old `cosign.pub` in `/etc/cloudfactory/cosign.pub`. They do not accept images or
agent updates that have a signature from the new key until you replace that file on each
server.

1. Do steps 2 to 5 above with a new key.
2. Replace `/etc/cloudfactory/cosign.pub` on each server with the new `cosign.pub`.
3. Restart the agent with `systemctl restart cf-agent`, so that it reads the new key. On the
   panel server, restart the panel container too.

## Make a release

1. Make sure that CI passes on `main`.
2. Start the release in one of these two ways:
   - **With git.** Make a tag with the version and push it:

     ```sh
     git fetch origin
     git tag v0.1.0 origin/main
     git push origin v0.1.0
     ```

     The workflow makes the GitHub release.
   - **On the GitHub website**, for example on a phone. Go to **Releases** > **Draft a new
     release**. In **Choose a tag**, type the new version, for example `v0.1.0`, and select
     **Create new tag**. Keep `main` as the target. Select **Generate release notes**, then
     **Publish release**. The workflow adds the files to this release when it finishes.
     Until then, the release has no files.

The workflow [`release.yml`](../.github/workflows/release.yml) then does these steps:

1. Build each image for x86-64 and ARM64.
2. Scan each image with Trivy. The release stops if Trivy finds a critical
   vulnerability that has a fix.
3. Push each image to `ghcr.io/<owner>` with the version tag.
4. Make an SBOM for each image. Sign each image and attach the SBOM with cosign.
5. Build `cf-agent` and `cf` for x86-64 and ARM64.
6. Put the version, the gVisor version, the registry and the public key into `install.sh`.
7. Sign each file and make a `.sha256` file for each file.
8. Make the GitHub release with all files, or add the files to the release that you made.

## Daily image rebuild

The workflow [`rebuild.yml`](../.github/workflows/rebuild.yml) runs each day. It checks the
digest of each base image. If a base image changed, it starts `release.yml` with
`images_only=true`. The new images get the tag `<version>-r<run number>`. The panel then
offers the new tag as an update.

## Change the gVisor version

Change `GVISOR_VERSION` in `release.yml` and the default value in
`agent/internal/config/config.go`. Use a release from
<https://gvisor.dev/docs/user_guide/install/>.
