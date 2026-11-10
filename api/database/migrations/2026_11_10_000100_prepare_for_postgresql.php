<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The panel now keeps its data in PostgreSQL (audit findings B4 and R4). SQLite does not check
 * the length of a text column, and PostgreSQL does: a value with more than 255 characters stops
 * the request with an error. These columns can get a long value from an agent or from a name that
 * the panel puts together, so they have no limit.
 */
return new class extends Migration
{
    /** @var array<string, list<array{string, bool, ?string}>> table => [column, nullable, default] */
    private const COLUMNS = [
        'audit_entries' => [['actor', false, null], ['action', false, null], ['target', false, '']],
        'agent_jobs' => [['title', false, null], ['ref', true, null]],
        'websites' => [['application', false, '']],
        'releases' => [['ref', false, null], ['message', false, '']],
        'snapshots' => [['restic_id', true, null]],
        'images' => [['check_error', true, null]],
    ];

    public function up(): void
    {
        foreach (self::COLUMNS as $name => $columns) {
            Schema::table($name, function (Blueprint $table) use ($columns) {
                foreach ($columns as [$column, $nullable, $default]) {
                    $definition = $table->text($column)->nullable($nullable);
                    if ($default !== null) {
                        $definition->default($default);
                    }
                    $definition->change();
                }
            });
        }
    }

    public function down(): void
    {
        foreach (self::COLUMNS as $name => $columns) {
            Schema::table($name, function (Blueprint $table) use ($columns) {
                foreach ($columns as [$column, $nullable, $default]) {
                    $definition = $table->string($column)->nullable($nullable);
                    if ($default !== null) {
                        $definition->default($default);
                    }
                    $definition->change();
                }
            });
        }
    }
};
