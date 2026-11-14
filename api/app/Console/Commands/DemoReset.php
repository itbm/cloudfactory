<?php

namespace App\Console\Commands;

use App\Providers\DemoServiceProvider;
use Database\Seeders\DemoSeeder;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/**
 * Makes the database again with the sample data of the demonstration mode. The command
 * deletes all data, so it runs only when the demonstration mode is on (CF_DEMO=true).
 */
#[Signature('demo:reset')]
#[Description('Delete all data and make the sample data of the demonstration mode')]
class DemoReset extends Command
{
    public function handle(): int
    {
        if (! DemoServiceProvider::enabled()) {
            $this->error('The demonstration mode is off. Set CF_DEMO=true in .env. A production panel cannot use it.');

            return self::FAILURE;
        }
        $this->call('migrate:fresh', ['--force' => true]);
        $this->call('db:seed', ['--class' => DemoSeeder::class, '--force' => true]);
        $this->info('The sample data is ready. Sign in as admin@example.com, reseller@example.com or owner@example.com with the password "'.DemoSeeder::PASSWORD.'".');

        return self::SUCCESS;
    }
}
