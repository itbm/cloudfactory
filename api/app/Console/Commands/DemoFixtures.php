<?php

namespace App\Console\Commands;

use App\Demo\Fixtures;
use Database\Seeders\DemoSeeder;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Artisan;
use Illuminate\Support\Facades\DB;

/**
 * Writes the fixtures for the tests of the web panel: the answers of the API with the sample
 * data, for one user of each role. The command uses a database in memory, so it does not
 * change the data of the developer.
 *
 * Run it after a change to an answer of the API or to the demo seeder. The test
 * DemoFixturesTest fails when the files do not agree with the API.
 */
#[Signature('demo:fixtures {directory? : The default is web/src/test/fixtures}')]
#[Description('Write the API answers that the tests of the web panel use')]
class DemoFixtures extends Command
{
    /** The date of the fixtures. A fixed date gives the same files each time. */
    public const NOW = '2026-11-14T10:00:00Z';

    public function handle(): int
    {
        $directory = $this->argument('directory') ?? base_path('../web/src/test/fixtures');
        if (! is_dir($directory) && ! mkdir($directory, 0755, true)) {
            $this->error("The command cannot make the directory {$directory}.");

            return self::FAILURE;
        }
        foreach (self::all() as $role => $answers) {
            file_put_contents("{$directory}/{$role}.json", json_encode($answers, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE)."\n");
            $this->line("Wrote {$directory}/{$role}.json");
        }

        return self::SUCCESS;
    }

    /**
     * The answers for each role, from a new database in memory with the sample data.
     *
     * @return array<string, array<string, array{status: int, body: mixed}>>
     */
    public static function all(): array
    {
        config(['database.connections.fixtures' => ['driver' => 'sqlite', 'database' => ':memory:', 'prefix' => '', 'foreign_key_constraints' => true]]);
        $previous = DB::getDefaultConnection();
        DB::setDefaultConnection('fixtures');
        Carbon::setTestNow(self::NOW);
        try {
            Artisan::call('migrate', ['--database' => 'fixtures', '--force' => true]);
            (new DemoSeeder)->run();

            return array_map(fn (string $role) => Fixtures::capture($role), array_combine(array_keys(Fixtures::USERS), array_keys(Fixtures::USERS)));
        } finally {
            Carbon::setTestNow();
            DB::purge('fixtures');
            DB::setDefaultConnection($previous);
        }
    }
}
