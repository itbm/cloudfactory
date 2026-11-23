<?php

namespace App\Demo;

/**
 * The output of a command in the demonstration mode (Run a command). The demo agent runs no
 * command. It gives a sample output for some usual commands, so that the command log shows
 * realistic results. A command with the word "fail" ends with exit code 1.
 */
class DemoCommands
{
    /** @return array{exitCode: int, output: string, timedOut: bool, durationMs: int} */
    public static function result(string $command): array
    {
        [$exit, $output] = self::output(trim($command));

        return ['exitCode' => $exit, 'output' => $output, 'timedOut' => false, 'durationMs' => 40 + crc32($command) % 2000];
    }

    /** @return array{int, string} */
    private static function output(string $command): array
    {
        $word = strtok($command, ' ') ?: '';

        return match (true) {
            str_contains($command, 'fail') => [1, "PHP Fatal error:  Uncaught RuntimeException: The command could not connect to the database in /app/src/Console/Kernel.php:42\nStack trace:\n#0 {main}\n  thrown in /app/src/Console/Kernel.php on line 42"],
            $command === 'php -v' => [0, "PHP 8.4.14 (cli) (built: Nov 18 2026 08:12:31) (NTS)\nCopyright (c) The PHP Group\nZend Engine v4.4.14, Copyright (c) Zend Technologies\n    with Zend OPcache v8.4.14, Copyright (c), by Zend Technologies"],
            str_starts_with($command, 'php artisan migrate') => [0, "\n   INFO  Running migrations.\n\n  2026_11_20_101500_add_status_to_orders_table ........................ 18.42ms DONE\n  2026_11_21_083000_create_coupons_table .............................. 11.07ms DONE\n"],
            str_starts_with($command, 'php artisan') => [0, "\n   INFO  Configuration cached successfully.\n"],
            $word === 'composer' => [0, "Installing dependencies from lock file\nVerifying lock file contents can be installed on current platform.\nNothing to install, update or remove\nGenerating optimized autoload files\n84 packages you are using are looking for funding.\nUse the `composer fund` command to find out more!"],
            str_starts_with($command, 'wp plugin list') => [0, "+--------------------+----------+-----------+---------+\n| name               | status   | update    | version |\n+--------------------+----------+-----------+---------+\n| akismet            | active   | none      | 5.5     |\n| woocommerce        | active   | available | 10.3.4  |\n| wordpress-seo      | active   | none      | 26.4    |\n+--------------------+----------+-----------+---------+"],
            $word === 'wp' => [0, 'Success: The command completed.'],
            $word === 'ls' => [0, "composer.json\ncomposer.lock\nconfig\npublic\nsrc\nstorage\nvendor"],
            $word === 'du' => [0, "182M\t."],
            default => [0, ''],
        };
    }
}
