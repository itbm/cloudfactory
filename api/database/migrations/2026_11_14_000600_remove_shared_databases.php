<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;

/**
 * Each database of a website is in a database server of the customer of the website. The
 * shared database containers of release 0.1.9 are not part of Cloudfactory now, and the panel
 * has no action that moves databases from them.
 *
 * - "database_instance" of a website goes. Each website uses the database servers of its
 *   customer on its server.
 * - A database or a database user with no database server was in the shared containers. The
 *   panel cannot operate it, so its record goes. This migration does not change a server: the
 *   data in the shared containers stays on the disk of the server.
 * - A job that moved databases from the shared containers goes, because the panel does not
 *   know its action now.
 */
return new class extends Migration
{
    public function up(): void
    {
        $users = DB::table('database_users')->whereNull('database_server_id');
        $databases = DB::table('databases')->whereNull('database_server_id');
        // The grants go first, for a database engine that does not remove them with the record.
        DB::table('database_grants')
            ->whereIn('database_user_id', (clone $users)->select('id'))
            ->orWhereIn('database_id', (clone $databases)->select('id'))
            ->delete();
        $users->delete();
        $databases->delete();
        DB::table('agent_jobs')->where('action', 'MoveDatabases')->delete();
        Schema::table('websites', fn (Blueprint $table) => $table->dropColumn('database_instance'));
    }

    public function down(): void
    {
        Schema::table('websites', fn (Blueprint $table) => $table->string('database_instance', 26)->nullable());
        // Each website used the database servers of its customer.
        DB::table('websites')->update(['database_instance' => DB::raw('customer_id')]);
    }
};
