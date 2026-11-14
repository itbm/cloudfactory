<?php

namespace Database\Seeders;

use Illuminate\Database\Console\Seeds\WithoutModelEvents;
use Illuminate\Database\Seeder;

/**
 * "php artisan migrate --seed" makes the sample data of the demonstration mode. Do not use this
 * data on a production panel.
 */
class DatabaseSeeder extends Seeder
{
    use WithoutModelEvents;

    public function run(): void
    {
        $this->call(DemoSeeder::class);
    }
}
