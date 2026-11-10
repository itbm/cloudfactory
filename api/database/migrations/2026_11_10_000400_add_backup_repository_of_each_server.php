<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;

/**
 * Audit finding S2: each server has its own restic repository and its own repository password.
 * A person who has root access to one server can then not read the backups of the websites
 * on the other servers.
 *
 * The snapshots that exist are in the repository that all servers used before. The panel
 * keeps them: a restore of such a snapshot uses the old repository and the old password.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('servers', function (Blueprint $table) {
            // Encrypted with the application key. The panel makes it at the first use.
            $table->text('backup_password')->nullable();
        });
        Schema::table('snapshots', function (Blueprint $table) {
            $table->boolean('shared_repository')->default(false);
        });
        DB::table('snapshots')->update(['shared_repository' => true]);
    }

    public function down(): void
    {
        Schema::table('snapshots', fn (Blueprint $table) => $table->dropColumn('shared_repository'));
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn('backup_password'));
    }
};
