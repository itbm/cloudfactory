<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The panel connects to the agent of each server to send a job. A new server can join with an
 * address that the panel cannot reach, for example a host name that is not in the DNS. The
 * panel records the reason, and the server page shows it. Null means no known problem.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('servers', function (Blueprint $table) {
            $table->string('connection_error', 500)->nullable();
            // The time of the last test of the connection. Null: the panel did no test yet.
            $table->timestamp('connection_checked_at')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn(['connection_error', 'connection_checked_at']));
    }
};
