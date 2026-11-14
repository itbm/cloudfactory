<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Second audit, finding F08: a server can have its own key for the object storage. The key
 * has access to the repository of the server only, and the panel sends it to that server
 * only. A person who has root access to one server can then not delete or change the
 * backups of the other servers.
 *
 * A server with no key of its own uses the key in the backup policy, as before.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('servers', function (Blueprint $table) {
            // The restic environment variables as JSON, encrypted with the application key.
            $table->text('backup_credentials')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn('backup_credentials'));
    }
};
