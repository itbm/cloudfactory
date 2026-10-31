<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Changes for the findings of the security assessment of release 0.1 (CF-01 to CF-21).
 */
return new class extends Migration
{
    public function up(): void
    {
        // CF-01: a database user can have access to more than one database, with a different
        // permission for each database.
        Schema::table('database_users', function (Blueprint $table) {
            $table->dropColumn('databases');
        });
        Schema::create('database_grants', function (Blueprint $table) {
            $table->foreignUlid('database_id')->constrained()->cascadeOnDelete();
            $table->foreignUlid('database_user_id')->constrained()->cascadeOnDelete();
            // "readonly" or "readwrite".
            $table->string('privileges');
            $table->primary(['database_id', 'database_user_id']);
        });

        // CF-06: the SHA-256 hash of each database dump at backup time.
        Schema::table('snapshots', function (Blueprint $table) {
            $table->json('dump_hashes')->nullable();
        });

        // CF-03: a customer proves that they control a domain before the edge proxy serves it.
        Schema::table('domains', function (Blueprint $table) {
            $table->string('verification_token', 64)->nullable();
            $table->timestamp('verified_at')->nullable();
        });

        // CF-12: the last TOTP time step that the user signed in with. The panel refuses it again.
        Schema::table('users', function (Blueprint $table) {
            $table->unsignedBigInteger('totp_last_step')->nullable();
        });

        // CF-10: only the agent on the panel server can call the panel API without the edge proxy.
        Schema::table('servers', function (Blueprint $table) {
            $table->boolean('local')->default(false);
        });
    }

    public function down(): void
    {
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn('local'));
        Schema::table('users', fn (Blueprint $table) => $table->dropColumn('totp_last_step'));
        Schema::table('domains', fn (Blueprint $table) => $table->dropColumn(['verification_token', 'verified_at']));
        Schema::table('snapshots', fn (Blueprint $table) => $table->dropColumn('dump_hashes'));
        Schema::dropIfExists('database_grants');
        Schema::table('database_users', fn (Blueprint $table) => $table->json('databases')->default('[]'));
    }
};
