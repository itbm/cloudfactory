<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The agent reports its website runtimes (the built-in runtimes and the runtimes from
 * manifest files) and its optional features, for example "valkey-backups". An agent of an
 * earlier release reports neither: the columns stay null.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('servers', function (Blueprint $table) {
            $table->json('agent_runtimes')->nullable();
            $table->json('agent_features')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('servers', function (Blueprint $table) {
            $table->dropColumn(['agent_runtimes', 'agent_features']);
        });
    }
};
