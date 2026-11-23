# Packs

This directory has the packs of Cloudfactory and two example packs. The contract of a pack is
in [docs/packs.md](../../docs/packs.md).

| Directory | Pack |
| --- | --- |
| [golang](golang/README.md) | An example pack for a publisher. It adds the runtime Go (`cf-runtime/1`). |
| [mysql](mysql/README.md) | An example database pack for a publisher. It adds the engine MySQL 8.4 (`cf-pack/2`, `cf-database/1`). The release does not publish it. |
| `php-legacy-frankenphp` | PHP 8.3 and 8.2 for FrankenPHP (`cf-php/1`). |
| `php-legacy-apache` | PHP 8.3 and 8.2 for Apache + PHP-FPM (`cf-php/1`). |

## The packs of older PHP versions

The two `php-legacy-*` packs add older versions of PHP to the built-in PHP runtimes. Use them
for a website that needs an older version, for example a website that comes from cPanel. Then
move the website to PHP 8.4 or later.

The images come from the same files as the built-in images:
[images/frankenphp](../../images/frankenphp/Containerfile) and
[images/apache-php-fpm](../../images/apache-php-fpm/Containerfile), with the build argument
`PHP_VERSION`.

The `pack.json` files in this directory are templates. Their version is `0.0.0`, and their
digests are zeros. The release workflow writes the version, the image tags and the digests of
the release, then signs each file with the release key. Install the files
`php-legacy-frankenphp.pack.json` and `php-legacy-apache.pack.json` from the release, with
their `.sig` files. Do not install the templates.

### Add a PHP version

1. Add the version to the matrix of the job `images` in
   [release.yml](../../.github/workflows/release.yml), and to
   [pack-images.yml](../../.github/workflows/pack-images.yml).
2. Add an image to `pack.json`, with the version, the reference, a digest of zeros and the
   last day of support of the PHP version.
3. Make sure that the base images exist for the version: `dunglas/frankenphp:1-phpX.Y-trixie`
   and `php:X.Y-fpm-trixie`.

The workflow `pack-images.yml` builds the images of a pull request. It checks that each image
has the PHP version of the pack, that each extension loads, and that the image starts with a
read-only root file system and no capabilities.
