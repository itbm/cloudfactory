<?php

namespace App\Demo;

use App\Enums\AgentAction;
use App\Models\AgentJob;
use App\Models\WordPressSite;

/**
 * The WordPress jobs of the demonstration agent (FR-206). The agent has no WordPress, so it
 * gives the versions of a small site, and an update gives the new versions.
 */
final class DemoWordPress
{
    /** The site that a new install gives. */
    public const NEW_SITE = [
        'version' => '6.8.1',
        'coreMinor' => '6.8.3',
        'coreLatest' => '6.9',
        'plugins' => [
            ['name' => 'akismet', 'title' => 'Akismet Anti-spam: Spam Protection', 'status' => 'inactive', 'version' => '5.4', 'update' => '5.5'],
            ['name' => 'hello', 'title' => 'Hello Dolly', 'status' => 'inactive', 'version' => '1.7.2'],
        ],
        'themes' => [
            ['name' => 'twentytwentyfive', 'title' => 'Twenty Twenty-Five', 'status' => 'active', 'version' => '1.2', 'update' => '1.3'],
            ['name' => 'twentytwentyfour', 'title' => 'Twenty Twenty-Four', 'status' => 'inactive', 'version' => '1.3'],
        ],
    ];

    /** @return array<string, mixed> */
    public static function result(AgentJob $job): array
    {
        $site = WordPressSite::where('website_id', $job->website_id)->first();
        $current = $site && $site->version ? self::inventoryOf($site) : self::withUrl(self::NEW_SITE, $job);
        $p = $job->payload;

        return match ($job->action) {
            AgentAction::InstallWordPress => ['wordpress' => self::withUrl(self::NEW_SITE, $job)],
            AgentAction::CheckWordPress, AgentAction::RestoreWordPress => ['wordpress' => $current],
            AgentAction::UpdateWordPress => ['before' => $current, 'wordpress' => self::updated($current, (string) ($p['core'] ?? ''), (array) ($p['plugins'] ?? []), (array) ($p['themes'] ?? []))],
            AgentAction::RunWpCli => ['exitCode' => 0, 'output' => self::output($current, $job), 'ranAt' => now()->toIso8601String(), 'wordpress' => $current],
            // The copy has the versions of the live site, at the address of the copy.
            AgentAction::StageWordPress => ['wordpress' => ['siteUrl' => (string) ($p['url'] ?? ''), 'checkedAt' => now()->toIso8601String()] + $current],
            default => [],
        };
    }

    /** @return array<string, mixed> */
    private static function inventoryOf(WordPressSite $site): array
    {
        return [
            'version' => $site->version, 'coreMinor' => $site->core_minor, 'coreLatest' => $site->core_latest,
            'siteUrl' => $site->site_url, 'plugins' => $site->plugins ?? [], 'themes' => $site->themes ?? [],
            'checkedAt' => now()->toIso8601String(),
        ];
    }

    /**
     * @param  array<string, mixed>  $inventory
     * @return array<string, mixed>
     */
    private static function withUrl(array $inventory, AgentJob $job): array
    {
        return $inventory + ['siteUrl' => 'https://'.$job->website?->name, 'checkedAt' => now()->toIso8601String()];
    }

    /**
     * The versions after an update: each updated item has the version of its update.
     *
     * @param  array<string, mixed>  $inventory
     * @return array<string, mixed>
     */
    private static function updated(array $inventory, string $core, array $plugins, array $themes): array
    {
        if ($core !== '') {
            $inventory['version'] = $core === 'latest' ? ($inventory['coreLatest'] ?? $inventory['coreMinor'] ?? $inventory['version']) : ($inventory['coreMinor'] ?? $inventory['version']);
            $inventory['coreMinor'] = null;
            $inventory['coreLatest'] = $core === 'latest' ? null : ($inventory['coreLatest'] ?? null);
        }
        foreach (['plugins' => $plugins, 'themes' => $themes] as $kind => $names) {
            $inventory[$kind] = array_map(fn (array $i) => in_array($i['name'], $names, true) && ($i['update'] ?? null)
                ? ['version' => $i['update'], 'update' => null] + $i : $i, $inventory[$kind] ?? []);
        }

        return $inventory;
    }

    /** @param array<string, mixed> $inventory */
    private static function output(array $inventory, AgentJob $job): string
    {
        $args = array_values(array_filter((array) ($job->payload['args'] ?? []), fn ($a) => is_string($a) && ! str_starts_with($a, '-')));

        return match (implode(' ', array_slice($args, 0, 2))) {
            'core version' => (string) $inventory['version'],
            'plugin list' => self::table($inventory['plugins'] ?? []),
            'theme list' => self::table($inventory['themes'] ?? []),
            default => 'Success: The demonstration agent ran the command. A real agent runs WP-CLI in the container of the website.',
        };
    }

    /** @param list<array<string, mixed>> $items */
    private static function table(array $items): string
    {
        $lines = ["name\tstatus\tupdate\tversion"];
        foreach ($items as $i) {
            $lines[] = "{$i['name']}\t{$i['status']}\t".(($i['update'] ?? null) ? 'available' : 'none')."\t{$i['version']}";
        }

        return implode("\n", $lines);
    }
}
