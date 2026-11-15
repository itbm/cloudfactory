<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * A website can have a database in a database server on a different server. The agent of
 * that server makes the backup of the database, in the repository of that server. Thus a
 * backup of a website can have more than one snapshot:
 *
 * - The part "website" has the files and the databases on the server of the website.
 * - A part "databases" has the databases on one different server.
 *
 * "server_id" is the server whose repository has the snapshot. Null is the server of the
 * website: all snapshots from before this change.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('snapshots', function (Blueprint $table) {
            $table->string('server_id', 26)->nullable();
            $table->string('part', 16)->default('website');
        });
        // The backups of a website that the panel deleted can be on more than one server.
        Schema::table('backup_removals', function (Blueprint $table) {
            $table->dropUnique(['website_id']);
            $table->unique(['website_id', 'server_id']);
        });
    }

    public function down(): void
    {
        Schema::table('backup_removals', function (Blueprint $table) {
            $table->dropUnique(['website_id', 'server_id']);
            $table->unique('website_id');
        });
        Schema::table('snapshots', fn (Blueprint $table) => $table->dropColumn(['server_id', 'part']));
    }
};
