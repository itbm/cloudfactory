<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * A website can have no public address.
 *
 * - "public": a website with domains. The edge proxy serves it. This is each website from
 *   before this migration.
 * - "private": a website with no domain and no route in the edge proxy. Only the websites of
 *   its networks can connect to it, by its network name on port 80.
 * - "worker": a background worker. Its container runs a command of the customer and serves no
 *   HTTP. It can connect to the members of its networks.
 *
 * Each kind is a website of the plan of the customer.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('websites', function (Blueprint $table) {
            $table->string('kind', 16)->default('public');
            // The start command of a worker: one command line for the shell of its container.
            $table->text('command')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('websites', fn (Blueprint $table) => $table->dropColumn(['kind', 'command']));
    }
};
