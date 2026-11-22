<?php

namespace App\Services\Packs;

use App\Enums\AgentAction;
use App\Enums\JobState;
use App\Enums\UpdatePolicy;
use App\Models\AgentJob;
use App\Models\Database;
use App\Models\Image;
use App\Models\Pack;
use App\Models\PluginKey;
use App\Models\Rollout;
use App\Models\Server;
use App\Models\Setting;
use App\Models\User;
use App\Models\Website;
use App\Services\Audit;
use App\Services\DatabaseEngines;
use App\Services\JobDispatcher;
use App\Services\Plugins\Signatures;
use App\Validation\InputRules;
use Illuminate\Support\Carbon;
use Illuminate\Support\Collection;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;

/**
 * Installs, upgrades and removes packs (docs/packs.md), and keeps the packs of each web server
 * the same as the packs of the panel.
 *
 * The panel installs a pack only when a trusted key signed its manifest, unless the developer
 * mode for packs is on. Each version of the runtime of a runtime pack becomes an image:
 * customers select it on the New website page. A database pack adds a database engine
 * (DatabaseEngines), with a block of host ports for its database servers. The panel sends all
 * its packs to each web server with one SetPacks job. The agent checks each signature again
 * and lets Podman pull an image of a pack only with a signature of the key of the publisher.
 */
class Packs
{
    /** After a try with no result, the panel waits 1 minute, then 2, then 4, up to this time. */
    public const RESYNC_MAX_MINUTES = 60;

    public function __construct(private JobDispatcher $jobs) {}

    public static function developerMode(): bool
    {
        return (bool) Setting::get('packs.developer_mode', false);
    }

    /**
     * The key that signed the manifest: a trusted key of the panel, or the release key of
     * Cloudfactory. Null when no key signed it.
     *
     * @return array{key: ?PluginKey, name: string, publicKey: string}|null
     */
    public static function signer(string $manifest, ?string $signature): ?array
    {
        if ($signature === null || trim($signature) === '') {
            return null;
        }
        $key = Signatures::signer($manifest, $signature);
        if ($key !== null) {
            return ['key' => $key, 'name' => $key->name, 'publicKey' => trim($key->public_key)."\n"];
        }
        $release = self::releaseKey();
        $raw = base64_decode(trim($signature), true);
        if ($release !== null && $raw !== false && $raw !== '' && strlen($raw) <= 256) {
            $public = @openssl_pkey_get_public($release);
            if ($public && openssl_verify($manifest, $raw, $public, OPENSSL_ALGO_SHA256) === 1) {
                return ['key' => null, 'name' => Pack::RELEASE_KEY, 'publicKey' => trim($release)."\n"];
            }
        }

        return null;
    }

    /** The release public key of Cloudfactory, when it is an ECDSA P-256 key. */
    private static function releaseKey(): ?string
    {
        $path = config('cloudfactory.cosign_public_key');
        $pem = is_string($path) && is_readable($path) ? file_get_contents($path) : false;
        if ($pem === false || trim($pem) === '') {
            return null;
        }
        try {
            Signatures::fingerprint($pem);
        } catch (ValidationException) {
            return null;
        }

        return $pem;
    }

    /**
     * Checks a manifest and its signature for an install or an upgrade.
     *
     * @return array{0: array<string, mixed>, 1: array{key: ?PluginKey, name: string, publicKey: string}|null}
     *
     * @throws ValidationException
     */
    public static function check(string $manifest, ?string $signature, ?Pack $upgrade = null): array
    {
        $spec = PackManifest::parse($manifest);
        $signer = self::signer($manifest, $signature);
        if ($signer === null && ! self::developerMode()) {
            throw ValidationException::withMessages(['signature' => $signature
                ? 'No trusted key signed this manifest. Add the public key of the publisher in Packs > Trusted keys, or check that the manifest has no change.'
                : 'Give the signature of the manifest. Only the developer mode accepts a pack with no signature.']);
        }
        $installed = Pack::where('slug', $spec['slug'])->first();
        if ($upgrade === null && $installed !== null) {
            throw ValidationException::withMessages(['manifest' => "The pack \"{$spec['slug']}\" is installed. To install a new version, update it."]);
        }
        if ($upgrade !== null && $spec['slug'] !== $upgrade->slug) {
            throw ValidationException::withMessages(['manifest' => "This manifest is for the pack \"{$spec['slug']}\", not \"{$upgrade->slug}\"."]);
        }
        if ($upgrade !== null && $spec['version'] === $upgrade->version) {
            throw ValidationException::withMessages(['manifest' => "The version {$spec['version']} is installed. Give a manifest with a new version."]);
        }
        // The websites of the images keep their runtime: an update cannot change a pack of a
        // new runtime into a PHP pack, or a PHP pack to a different PHP runtime. The database
        // servers of a database pack keep their engine.
        if ($upgrade !== null && $upgrade->kind !== $spec['kind']) {
            throw ValidationException::withMessages(['manifest' => "The new version is a {$spec['kind']} pack, and the installed version is a {$upgrade->kind} pack. An update of a pack cannot change its kind."]);
        }
        if ($upgrade !== null) {
            $before = $upgrade->spec()['runtime'];
            if ($before['contract'] !== $spec['runtime']['contract'] || $before['family'] !== $spec['runtime']['family']) {
                throw ValidationException::withMessages(['manifest' => 'The new version has a different runtime. An update of a pack cannot change its runtime contract or its PHP runtime.']);
            }
        }
        if ($upgrade === null && Pack::count() >= InputRules::MAX_PACKS) {
            throw ValidationException::withMessages(['manifest' => sprintf('The panel can have %d packs.', InputRules::MAX_PACKS)]);
        }
        // An image repository is in one pack only: the signature policy of a server has one
        // key for each repository. An image of the panel cannot have the repository either. A
        // database pack has no image records, so the panel reads its manifest.
        foreach (Pack::where('kind', PackManifest::KIND_DATABASE)->where('id', '!=', $upgrade?->id ?? '')->get() as $other) {
            $repositories = array_column($other->spec()['images'], 'repository');
            foreach ($spec['images'] as $img) {
                if (in_array($img['repository'], $repositories, true)) {
                    throw ValidationException::withMessages(['manifest' => "The image {$img['repository']} is in the pack {$other->slug}. An image repository can be in one pack only."]);
                }
            }
        }
        if (PackManifest::isDatabase($spec) && in_array($spec['slug'], Database::ENGINES, true)) {
            throw ValidationException::withMessages(['manifest' => "The pack name \"{$spec['slug']}\" is the name of a built-in database engine."]);
        }
        if ($upgrade === null && PackManifest::isDatabase($spec) && DatabaseEngines::freeBlock() === null) {
            throw ValidationException::withMessages(['manifest' => 'The panel has no free block of ports for a new database pack. Remove a database pack first.']);
        }
        foreach ($spec['images'] as $img) {
            $other = Image::where('reference', $img['repository'])
                ->where(fn ($q) => $q->whereNull('pack_id')->orWhere('pack_id', '!=', $upgrade?->id ?? ''))
                ->with('pack')->first();
            if ($other !== null) {
                throw ValidationException::withMessages(['manifest' => $other->pack
                    ? "The image {$img['repository']} is in the pack {$other->pack->slug}. An image repository can be in one pack only."
                    : "The image {$img['repository']} is the image {$other->name} of the panel. A pack needs its own image repository."]);
            }
        }
        // A PHP pack adds a version of PHP only when the runtime does not have it, so that the
        // New website page has one image for each version of each runtime.
        if (PackManifest::isPhp($spec)) {
            foreach ($spec['images'] as $img) {
                $other = Image::where('family', $spec['runtime']['family'])->where('php_version', $img['version'])
                    ->where(fn ($q) => $q->whereNull('pack_id')->orWhere('pack_id', '!=', $upgrade?->id ?? ''))->first();
                if ($other !== null) {
                    throw ValidationException::withMessages(['manifest' => "The panel has the image {$other->name} already. A PHP pack can add only a version of PHP that the runtime does not have."]);
                }
            }
        }
        if ($upgrade !== null) {
            self::checkRemovedVersions($upgrade, $spec);
        }

        return [$spec, $signer];
    }

    /**
     * An upgrade can remove a version of the runtime only when no website uses it, and a
     * version of a database engine only when no database server uses it.
     *
     * @throws ValidationException
     */
    private static function checkRemovedVersions(Pack $pack, array $spec): void
    {
        $keep = array_column($spec['images'], 'version');
        $servers = $pack->isDatabase() ? $pack->databaseServers()->whereNotIn('engine_version', $keep)->orderBy('name')->get() : collect();
        if ($servers->isNotEmpty()) {
            $first = $servers->first();
            throw ValidationException::withMessages(['manifest' => "The new version has no image for {$pack->name} {$first->engine_version}, and the database server {$first->name} uses it. Remove the database servers of that version first."]);
        }
        foreach ($pack->images()->whereNotIn('runtime_version', $keep)->get() as $image) {
            if ($image->websites()->exists()) {
                throw ValidationException::withMessages(['manifest' => "The new version has no image for {$image->name}, and websites use it. Change the runtime of these websites first."]);
            }
        }
    }

    /** Installs a new pack and sends the packs to each web server. */
    public function install(string $manifest, ?string $signature, ?User $user): Pack
    {
        [$spec, $signer] = self::check($manifest, $signature);
        $pack = DB::transaction(function () use ($manifest, $signature, $signer, $spec, $user) {
            $pack = Pack::create([
                ...self::fields($spec, $manifest, $signature, $signer), 'user_id' => $user?->id,
                // The database servers of the engine get the ports of this block.
                'database_port' => PackManifest::isDatabase($spec) ? DatabaseEngines::freeBlock() : null,
            ]);
            self::syncImages($pack, $spec);

            return $pack;
        });
        $this->pushAll("Install pack {$pack->name} {$pack->version}");

        return $pack;
    }

    /**
     * Installs a new version of a pack. A new image version that no website uses changes at
     * once. For an image with websites, the new digest waits as an update of the image: an
     * administrator rolls it out on the Apps page.
     */
    public function upgrade(Pack $pack, string $manifest, ?string $signature): Pack
    {
        [$spec, $signer] = self::check($manifest, $signature, $pack);
        DB::transaction(function () use ($pack, $manifest, $signature, $signer, $spec) {
            $pack->update(self::fields($spec, $manifest, $signature, $signer));
            self::syncImages($pack, $spec);
        });
        $this->pushAll("Update pack {$pack->name} to {$pack->version}");

        return $pack;
    }

    /**
     * Removes a pack and its images. The panel refuses when a website uses an image of the pack.
     *
     * @throws ValidationException
     */
    public function remove(Pack $pack): void
    {
        if ($pack->isDatabase() && ($servers = $pack->databaseServers()->count()) > 0) {
            throw ValidationException::withMessages(['pack' => "{$servers} database ".($servers === 1 ? 'server uses' : 'servers use')." the engine {$pack->name}. Remove these database servers first."]);
        }
        $ids = $pack->images()->pluck('id');
        $used = Website::whereIn('image_id', $ids)->count();
        if ($used > 0) {
            throw ValidationException::withMessages(['pack' => "{$used} ".($used === 1 ? 'website uses' : 'websites use')." the runtime {$pack->name}. Change the runtime of these websites or remove them first."]);
        }
        DB::transaction(function () use ($pack, $ids) {
            Rollout::whereIn('image_id', $ids)->delete();
            Image::whereIn('id', $ids)->delete();
            $pack->delete();
        });
        $this->pushAll("Remove pack {$pack->name}");
    }

    /** @return array<string, mixed> */
    private static function fields(array $spec, string $manifest, ?string $signature, ?array $signer): array
    {
        return [
            'slug' => $spec['slug'], 'name' => $spec['name'], 'version' => $spec['version'], 'publisher' => $spec['publisher'], 'kind' => $spec['kind'],
            'description' => $spec['description'] === '' ? null : $spec['description'], 'manifest' => $manifest,
            'signature' => $signer ? trim((string) $signature) : null, 'public_key' => $signer['publicKey'] ?? null,
            'plugin_key_id' => $signer['key']?->id ?? null, 'signer' => $signer['name'] ?? null,
        ];
    }

    /**
     * Makes the images of the pack agree with its manifest. An image of a pack of a new runtime
     * has the family of the pack. An image of a PHP pack has the family of its built-in PHP
     * runtime, so that it is one more version of that runtime for the websites.
     */
    public static function syncImages(Pack $pack, array $spec): void
    {
        // A database pack has no images for websites. Each database server keeps its version,
        // and the agent takes the image of the version from the manifest.
        if (PackManifest::isDatabase($spec)) {
            return;
        }
        $php = PackManifest::isPhp($spec);
        $keep = [];
        foreach ($spec['images'] as $img) {
            $keep[] = $img['version'];
            $fields = [
                'name' => $php ? Image::PHP_LABELS[$spec['runtime']['family']]." · PHP {$img['version']}" : "{$spec['name']} {$img['version']}",
                'family' => $php ? $spec['runtime']['family'] : $spec['slug'], 'reference' => $img['repository'],
                'runtime_version' => $img['version'], 'php_version' => $php ? $img['version'] : null,
                'support_ends_at' => $img['supportEndsAt'], 'custom' => false,
                'default_command' => $php ? null : $spec['runtime']['defaultCommand'], 'default_install' => $php ? null : ($spec['runtime']['defaultInstall'] ?: null),
                // The signature of the manifest covers the digest. Each server checks the
                // signature of the image with the key of the publisher when it pulls the image.
                'signature_verified' => $pack->signed(),
            ];
            $image = $pack->images()->where('runtime_version', $img['version'])->first();
            if ($image === null) {
                $pack->images()->create([...$fields, 'tag' => $img['tag'], 'digest' => $img['digest'], 'policy' => UpdatePolicy::Manual]);

                continue;
            }
            $image->update($fields);
            if ($image->tag === $img['tag'] && $image->digest === $img['digest']) {
                $image->update(['available_tag' => null, 'available_digest' => null]);
            } elseif ($image->inUse()) {
                $image->update(['available_tag' => $img['tag'], 'available_digest' => $img['digest']]);
                Audit::system('Found image update', "{$image->name}: new digest {$img['digest']} from the pack {$spec['slug']} {$spec['version']}");
            } else {
                $image->changeVersion($img['tag'], $img['digest']);
            }
        }
        $pack->images()->whereNotIn('runtime_version', $keep)->get()->each(function (Image $image) {
            Rollout::where('image_id', $image->id)->delete();
            $image->delete();
        });
    }

    /**
     * The packs that each web server must have, as the agent gets them.
     *
     * @return list<array{manifest: string, signature: string, publicKey: string}>
     */
    public static function forAgent(): array
    {
        return Pack::orderBy('slug')->get()->map(fn (Pack $p) => $p->forAgent())->all();
    }

    /** @return Collection<int, Server> The servers that get the packs. */
    public static function servers(): Collection
    {
        return Server::whereJsonContains('roles', 'web')->whereNotIn('state', ['joining'])->orderBy('name')->get();
    }

    /** Sends all packs to each web server. */
    public function pushAll(string $why): void
    {
        foreach (self::servers() as $server) {
            $server->update(['packs_resync' => null]);
            $this->send($server, "{$why} on {$server->name}");
        }
    }

    /** The job has no packs: AgentClient adds the packs of the panel when it sends the job. */
    private function send(Server $server, string $title): AgentJob
    {
        return $this->jobs->dispatch($server, AgentAction::SetPacks, [], $title);
    }

    /**
     * The agent reported its packs. When they are not the packs of the panel, for example
     * after the server was offline or after a failed job, the panel sends the packs again.
     * The panel does nothing while a SetPacks job of the server is in the queue or runs. After
     * a try with no result, it waits 1 minute, then 2, then 4, up to RESYNC_MAX_MINUTES.
     *
     * @param  list<array{slug: string, version: string}>  $reported
     */
    public function reconcile(Server $server, array $reported): void
    {
        if (! in_array('web', $server->roles ?? [], true)) {
            return;
        }
        $has = collect($reported)->mapWithKeys(fn (array $p) => [(string) $p['slug'] => (string) $p['version']])->sortKeys()->all();
        $want = Pack::orderBy('slug')->pluck('version', 'slug')->sortKeys()->all();
        if ($has === $want) {
            if ($server->packs_resync !== null) {
                $server->update(['packs_resync' => null]);
            }

            return;
        }
        if (self::busy($server)) {
            return;
        }
        $tries = (int) ($server->packs_resync['tries'] ?? 0);
        $last = $server->packs_resync['at'] ?? null;
        $wait = $tries === 0 ? 0 : min(self::RESYNC_MAX_MINUTES, 2 ** ($tries - 1));
        if ($last !== null && now()->lt(Carbon::parse($last)->addMinutes($wait))) {
            return;
        }
        $server->update(['packs_resync' => ['at' => now()->toIso8601String(), 'tries' => $tries + 1]]);
        $this->send($server, "Send the packs to {$server->name} again");
    }

    /** True while a SetPacks job of the server is in the queue or runs. */
    private static function busy(Server $server): bool
    {
        return AgentJob::where('server_id', $server->id)->where('action', AgentAction::SetPacks->value)
            ->whereIn('state', [JobState::Queued->value, JobState::Running->value])->exists();
    }

    /**
     * A SetPacks job succeeded: the panel keeps the packs and the runtimes of the server, so
     * that a new website of a pack can go to the server before its next report.
     */
    public static function jobFinished(AgentJob $job, array $result): void
    {
        $server = $job->server;
        $update = [];
        if (is_array($result['packs'] ?? null)) {
            $update['agent_packs'] = array_values(array_map(fn ($p) => ['slug' => (string) ($p['slug'] ?? ''), 'version' => (string) ($p['version'] ?? '')],
                array_filter($result['packs'], 'is_array')));
        }
        if (is_array($result['runtimes'] ?? null)) {
            $update['agent_runtimes'] = array_values(array_filter($result['runtimes'], 'is_string'));
        }
        if ($update !== []) {
            $server->update($update);
        }
    }

    /** The state of the pack on each web server: installed, waiting or with another version. */
    public static function serverStates(Pack $pack): array
    {
        return self::servers()->map(function (Server $s) use ($pack) {
            $has = collect($s->agent_packs ?? [])->firstWhere('slug', $pack->slug);

            return [
                'id' => $s->id, 'name' => $s->name,
                'version' => $has['version'] ?? null,
                'state' => match (true) {
                    ($has['version'] ?? null) === $pack->version => 'installed',
                    $s->packs_resync !== null && (int) ($s->packs_resync['tries'] ?? 0) > 1 => 'failing',
                    default => 'pending',
                },
            ];
        })->values()->all();
    }
}
