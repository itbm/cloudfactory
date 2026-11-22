<?php

namespace App\Services\Imports;

use App\Models\DnsZone;
use App\Models\Domain;
use App\Models\Image;
use App\Models\Import;
use App\Models\MailDomain;
use App\Models\Website;
use Illuminate\Support\Collection;

/**
 * The plan of an import: what the import makes from the description of the account, and what
 * it cannot make. The user sees the plan before the import starts, and selects the parts.
 */
final class Planner
{
    /**
     * @return array{websites: list<array<string, mixed>>, mailDomains: list<array<string, mixed>>, mailSpace: array{mailboxes: int, bytes: int}|null, dnsZones: list<array<string, mixed>>, images: list<array<string, mixed>>}
     */
    public static function plan(Import $import): array
    {
        $d = $import->description ?? [];
        $images = self::phpImages();
        $websites = [];
        foreach ($d['websites'] ?? [] as $w) {
            $image = self::imageFor($images, $w['runtime']['phpVersion'] ?? '');
            $websites[] = [
                'name' => $w['name'],
                'domains' => $w['domains'],
                'phpVersion' => $w['runtime']['phpVersion'] ?: null,
                'imageId' => $image?->id,
                'imageName' => $image?->name,
                'diskBytes' => $w['diskBytes'],
                'files' => $w['content']['files'] !== null,
                'databases' => array_map(fn ($db) => ['name' => $db['name'], 'bytes' => $db['content']['bytes'] ?? 0], $w['databases']),
                'databaseUsers' => count($w['databaseUsers']),
                'cronJobs' => count($w['cronJobs']),
                'wordpress' => $w['appConfig'] !== null,
                'conflict' => self::websiteConflict($import, $w),
            ];
        }
        $mail = [];
        foreach ($d['mail']['domains'] ?? [] as $m) {
            $existing = MailDomain::where('name', $m['name'])->first();
            $mail[] = [
                'name' => $m['name'],
                'mailboxes' => count($m['mailboxes']),
                'quotaBytes' => array_sum(array_map(Runner::quota(...), $m['mailboxes'])),
                'aliases' => count($m['aliases']),
                'messages' => array_sum(array_column($m['mailboxes'], 'messages')),
                'bytes' => array_sum(array_map(fn ($b) => $b['content']['bytes'] ?? 0, $m['mailboxes'])),
                'newPasswords' => count(array_filter($m['mailboxes'], fn ($b) => $b['passwordHash'] === '')),
                'conflict' => $existing ? 'This domain is a mail domain on the panel already.' : null,
            ];
        }
        $zones = [];
        foreach ($d['dnsZones'] ?? [] as $z) {
            $existing = DnsZone::where('name', $z['name'])->first();
            $zones[] = [
                'name' => $z['name'],
                // A zone of the customer gets the records that it does not have.
                'exists' => $existing !== null && $existing->customer_id === $import->customer_id,
                'conflict' => $existing && $existing->customer_id !== $import->customer_id ? 'A different customer has this DNS zone.' : null,
            ];
        }

        // The mailboxes that do not fit in the plan of the customer fail.
        $plan = $import->customer?->plan;
        $used = $import->customer ? $import->customer->mailboxes() : null;

        return [
            'websites' => $websites,
            'mailDomains' => $mail,
            'mailSpace' => $plan && $used ? [
                'mailboxes' => max(0, $plan->mailboxes - $used->count()),
                'bytes' => max(0, $plan->mail_storage_bytes - (int) $used->sum('quota_bytes')),
            ] : null,
            'dnsZones' => $zones,
            'images' => $images->map(fn (Image $i) => ['id' => $i->id, 'name' => $i->name, 'phpVersion' => $i->php_version])->values()->all(),
        ];
    }

    /** The reason that the import cannot make a website, or null. */
    private static function websiteConflict(Import $import, array $w): ?string
    {
        if (Website::where('name', $w['name'])->exists()) {
            return 'A website with this name exists.';
        }
        $names = array_column($w['domains'], 'name');
        $taken = Domain::whereIn('name', $names)->value('name');

        return $taken ? "The domain {$taken} is a domain of a website on the panel already." : null;
    }

    /**
     * The official PHP images. Apache first: the websites of cPanel, DirectAdmin and Plesk often
     * have .htaccess files.
     *
     * @return Collection<int, Image>
     */
    public static function phpImages()
    {
        return Image::whereIn('family', ['apache-php-fpm', 'frankenphp'])->where('custom', false)->whereNotNull('php_version')->get()
            ->sortBy([fn ($a, $b) => ($a->family === 'apache-php-fpm' ? 0 : 1) <=> ($b->family === 'apache-php-fpm' ? 0 : 1), fn ($a, $b) => version_compare($a->php_version, $b->php_version)])
            ->values();
    }

    /**
     * The image for a PHP version of the old panel: the same version, else the first newer
     * version, else the newest version. Apache comes before FrankenPHP.
     *
     * @param  Collection<int, Image>  $images
     */
    public static function imageFor($images, string $php): ?Image
    {
        if ($images->isEmpty()) {
            return null;
        }
        $apache = $images->where('family', 'apache-php-fpm')->values();
        $pool = $apache->isNotEmpty() ? $apache : $images;
        if ($php !== '') {
            if ($same = $pool->first(fn (Image $i) => $i->php_version === $php)) {
                return $same;
            }
            if ($newer = $pool->first(fn (Image $i) => version_compare($i->php_version, $php) > 0)) {
                return $newer;
            }
        }

        return $pool->last();
    }
}
