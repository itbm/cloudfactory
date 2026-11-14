<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Valkey is the third database engine. A Valkey database is one database number of the shared
 * Valkey server of the server. The agent gives the number, and the application needs it to
 * connect. Valkey does not know the size of one database in bytes, so the panel shows its
 * number of keys.
 *
 * The agent reports the database engines that it can operate. An agent of an earlier release
 * does not: it has MariaDB and PostgreSQL only.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('databases', function (Blueprint $table) {
            $table->unsignedInteger('number')->nullable();
            $table->unsignedBigInteger('key_count')->nullable();
        });
        Schema::table('servers', function (Blueprint $table) {
            $table->json('database_engines')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('databases', fn (Blueprint $table) => $table->dropColumn(['number', 'key_count']));
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn('database_engines'));
    }
};
