<?php

namespace App\Console\Commands;

use App\Models\User;
use App\Providers\DemoServiceProvider;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;
use Illuminate\Support\Facades\Schema;
use Symfony\Component\Process\Process;

/**
 * Starts the panel API of the demonstration mode with one command: the HTTP server and the
 * queue worker that runs the demo agent. With --fresh, or with an empty database, the command
 * makes the sample data first.
 */
#[Signature('demo:serve {--fresh : Delete all data and make the sample data again} {--host=127.0.0.1} {--port=8000}')]
#[Description('Start the API and the demo agent with the sample data')]
class DemoServe extends Command
{
    public function handle(): int
    {
        if (! DemoServiceProvider::enabled()) {
            $this->error('The demonstration mode is off. Set CF_DEMO=true in .env. A production panel cannot use it.');

            return self::FAILURE;
        }
        // The command makes the data before it starts the worker: a worker cannot use an SQLite
        // file while a different process makes the file again.
        if ($this->option('fresh') || ! Schema::hasTable('users') || User::count() === 0) {
            if ($this->call('demo:reset') !== self::SUCCESS) {
                return self::FAILURE;
            }
        }

        $artisan = [PHP_BINARY, base_path('artisan')];
        $processes = [
            new Process([...$artisan, 'serve', '--host='.$this->option('host'), '--port='.$this->option('port')], base_path(), null, null, null),
            new Process([...$artisan, 'queue:work', '--sleep=1', '--tries=1'], base_path(), null, null, null),
        ];
        foreach ($processes as $process) {
            $process->start(fn (string $type, string $output) => $this->output->write($output));
        }
        $this->info("The API of the demonstration mode listens on http://{$this->option('host')}:{$this->option('port')}. Press Ctrl+C to stop.");

        // Stop when one of the two processes stops, and stop the other one too.
        while (collect($processes)->every(fn (Process $p) => $p->isRunning())) {
            usleep(250_000);
        }
        foreach ($processes as $process) {
            $process->stop(3);
        }

        return self::FAILURE;
    }
}
