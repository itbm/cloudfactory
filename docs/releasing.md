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

Do not select **Set as a pre-release** for a release that the installation command of the
README must find. GitHub has the address `releases/latest/download/install.sh` only for a
release that is not a pre-release. While each release is a pre-release, that address gives
error 404, and a new user must put the version in the address. The **Add server** command of
the panel always uses the version of the panel, so it works in the two cases.

The workflow [`release.yml`](../.github/workflows/release.yml) then does these steps:

1. Build each image for x86-64 and ARM64. The build uses the layers of the last build from
   the build cache (see [Build cache](#build-cache)).
2. Push each image to `ghcr.io/<owner>` by its digest only. The image has no tag yet.
3. Scan each image with Trivy. The release stops if Trivy finds a critical
   vulnerability that has a fix.
4. Make an SBOM for each image. Sign each image and attach the SBOM with cosign.
5. Build `cf-agent` and `cf` for x86-64 and ARM64.
6. Put the version, the gVisor version, the registry and the public key into `install.sh`.
7. Sign each file and make a `.sha256` file for each file.
8. When all of the steps above are successful, the last job does these steps:
   1. Verify the signature of each image with `cosign.pub`.
   2. Give each image the version tag, for example `0.1.0`.
   3. Make the GitHub release with all files, or add the files to the release that you made.

If a build fails, the workflow does not tag an image and does not add a file to the release.
Fix the problem, then run the failed jobs again from the **Actions** tab. If the fix needs a
new commit, delete the release and the tag, then make the release again.

## Package visibility

Servers pull the images with no login. Thus, before production, each image package on
`ghcr.io/<owner>` must be public.

A package that the release workflow makes for the first time can get a visibility that is not
the same as the visibility of the repository or of the other packages. Thus, after a release
adds a new image, check the visibility of its package. For example, the first release with
Valkey makes the package `valkey`. To make a package public:

1. Open `https://github.com/users/<owner>/packages/container/<package>/settings`.
2. In **Danger Zone**, select **Change visibility**, then **Public**.

Each image job of the release workflow checks its package and the package `build-cache` first.
If a package is not public, the job gives a warning with the link to its settings. The job does
not stop.

The publish job does the check again after it adds the tags. If an image package is not public,
the job stops with an error before it publishes the release, because a server cannot pull the
image: a website with that image fails with `unauthorized`. Make the package public, then run
the publish job again from the **Actions** tab.

## Build cache

The image builds keep their layers in the package `ghcr.io/<owner>/build-cache`, with one tag
for each image. The next build uses these layers again when their inputs did not change. This
makes the PHP images much faster, because the build does not compile the PHP extensions again.

Each Containerfile installs the security fixes of Debian or Alpine in a step of its own. The
workflow gives the build argument `CF_REFRESH` a new value for each run, and each Containerfile
declares `CF_REFRESH` immediately before that step. Thus this step, and all steps after it,
never come from the cache. Each release and each rebuild gets the security fixes of that day.

To build all layers again, delete the package `build-cache` on GitHub. The next release makes
it again. The build cache is not part of a release. Podman and the panel do not use it.

## Daily image rebuild

**Note:** The daily schedule is off until production, to save CI minutes. To run the check one
time, start **Rebuild images** from the **Actions** tab. To turn the schedule on again, remove
the `#` from the `schedule` lines in `rebuild.yml`.

The workflow [`rebuild.yml`](../.github/workflows/rebuild.yml) runs each day. It checks the
digest of each base image. If a base image changed, it starts `release.yml` with
`images_only=true`. The new images get the tag `<version>-r<run number>`. The panel then
offers the new tag as an update.

## Change the gVisor version

Change `GVISOR_VERSION` in `release.yml` and the default value in
`agent/internal/config/config.go`. Use a release from
<https://gvisor.dev/docs/user_guide/install/>.
