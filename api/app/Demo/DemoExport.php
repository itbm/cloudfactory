<?php

namespace App\Demo;

use App\Models\AccountExport;
use App\Models\AccountExportPart;
use App\Models\AgentJob;
use App\Models\Customer;
use App\Models\User;
use App\Models\Website;
use App\Services\Exports\AccountExports;
use App\Services\Exports\Description;
use Illuminate\Support\Carbon;

/**
 * The demo agent sends the parts of an account export to the panel, through the same
 * AccountExports::receive as a real agent: the files of the website from the demo file tree,
 * and a short example dump of each database.
 */
class DemoExport
{
    /** @return array{parts: array<string, array{sizeBytes: int, sha256: string}>} */
    public static function send(AgentJob $job): array
    {
        $p = $job->payload;
        $website = Website::find($p['websiteId'] ?? '');
        $parts = [];
        if (($p['filesPart'] ?? '') !== '' && $website) {
            $parts[$p['filesPart']] = self::part($job, $p['filesPart'], DemoSite::exportArchive($website));
        }
        foreach ($p['databases'] ?? [] as $db) {
            $name = (string) ($db['name'] ?? '');
            $parts[$db['part']] = self::part($job, (string) $db['part'], self::dump((string) ($db['engine'] ?? ''), $name));
        }

        return ['parts' => $parts];
    }

    /**
     * A complete export for the sample data (DemoSeeder): the panel makes it at $at from the
     * demo files and the example dumps, with no jobs.
     */
    public static function seed(Customer $customer, ?User $user, Carbon $at): AccountExport
    {
        // The command demo:fixtures sets a fixed time. The seed keeps it.
        $previous = Carbon::getTestNow();
        Carbon::setTestNow($at);
        try {
            $export = AccountExport::create([
                'customer_id' => $customer->id, 'user_id' => $user?->id, 'state' => AccountExport::RUNNING,
                'files' => true, 'databases' => true, 'description' => Description::of($customer, true, true),
            ]);
            foreach ($export->description['websites'] as $w) {
                $website = Website::where('customer_id', $customer->id)->where('name', $w['name'])->firstOrFail();
                if ($path = $w['content']['files']['path'] ?? null) {
                    self::write($export, $website, $path, DemoSite::exportArchive($website));
                }
                foreach ($w['databases'] as $db) {
                    if ($path = $db['content']['path'] ?? null) {
                        self::write($export, $website, $path, self::dump($db['engine'], $db['name']));
                    }
                }
            }
            Carbon::setTestNow($at->copy()->addMinutes(3));
            AccountExports::build($export);
        } finally {
            Carbon::setTestNow($previous);
        }

        return $export->fresh();
    }

    private static function write(AccountExport $export, Website $website, string $path, string $content): void
    {
        $part = AccountExportPart::create(['account_export_id' => $export->id, 'server_id' => $website->server_id, 'path' => $path,
            'received_bytes' => strlen($content), 'size_bytes' => strlen($content), 'sha256' => hash('sha256', $content)]);
        $file = AccountExports::partFile($part);
        if (! is_dir(dirname($file))) {
            mkdir(dirname($file), 0700, true);
        }
        file_put_contents($file, $content);
    }

    /** A short example dump. A real export of PostgreSQL has the custom format of pg_dump. */
    private static function dump(string $engine, string $name): string
    {
        return $engine === 'mariadb'
            ? (string) gzencode("-- MariaDB dump of the database {$name} (demonstration)\nCREATE TABLE `orders` (`id` int NOT NULL, `total` decimal(10,2) NOT NULL, PRIMARY KEY (`id`));\nINSERT INTO `orders` VALUES (1,19.99),(2,42.00);\n")
            : "PGDMP (demonstration) The dump of the PostgreSQL database {$name}.\n";
    }

    /** @return array{sizeBytes: int, sha256: string} */
    private static function part(AgentJob $job, string $partId, string $content): array
    {
        $exports = app(AccountExports::class);
        foreach (str_split($content, AccountExports::CHUNK_BYTES) ?: [''] as $i => $piece) {
            $body = fopen('php://memory', 'w+b');
            fwrite($body, $piece);
            rewind($body);
            $exports->receive($job->server, $partId, $i * AccountExports::CHUNK_BYTES, $body);
            fclose($body);
        }

        return ['sizeBytes' => strlen($content), 'sha256' => hash('sha256', $content)];
    }
}
