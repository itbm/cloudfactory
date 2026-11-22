<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * An administrator can move a website to a different server. The website keeps its ID, its
 * Unix user, its domains and its network name. Its files go through the tunnel between the
 * two servers.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('websites', function (Blueprint $table) {
            // The server that receives the website, while the status is "moving".
            $table->foreignUlid('moving_to_server_id')->nullable()->constrained('servers')->nullOnDelete();
        });

        Schema::table('backup_removals', function (Blueprint $table) {
            // True for a website that moved: the old server removes only the backups of its
            // files. The backups of the databases on that server stay.
            $table->boolean('files_only')->default(false);
        });
    }

    public function down(): void
    {
        Schema::table('backup_removals', fn (Blueprint $table) => $table->dropColumn('files_only'));
        Schema::table('websites', function (Blueprint $table) {
            $table->dropConstrainedForeignId('moving_to_server_id');
        });
    }
};
