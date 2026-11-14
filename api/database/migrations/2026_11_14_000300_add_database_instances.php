<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Each customer has its own database containers on a server: one MariaDB container, one
 * PostgreSQL container and one Valkey container. A customer cannot change the service of a
 * different customer.
 *
 * "database_instance" of a website is the ID of the customer whose database containers the
 * website uses. It is null for a website that uses the shared database containers of an
 * earlier release. Such a website gets the containers of its customer when an administrator
 * moves the databases of its server.
 *
 * "database_instances" of a server is true when its agent reports that it can operate the
 * database containers of each customer.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('websites', function (Blueprint $table) {
            $table->string('database_instance', 26)->nullable();
        });
        Schema::table('servers', function (Blueprint $table) {
            $table->boolean('database_instances')->default(false);
        });
    }

    public function down(): void
    {
        Schema::table('websites', fn (Blueprint $table) => $table->dropColumn('database_instance'));
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn('database_instances'));
    }
};
