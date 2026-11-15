<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * An administrator can move a database server to a different server. The database server
 * keeps its ID, its name and its network address. Its data goes through the tunnel between
 * the two servers.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('database_servers', function (Blueprint $table) {
            // The server that receives the database server, while the status is "moving".
            $table->foreignUlid('moving_to_server_id')->nullable()->constrained('servers')->nullOnDelete();
        });

        Schema::table('servers', function (Blueprint $table) {
            // True when the agent reported that it can send and receive a database server.
            $table->boolean('mesh_moves')->default(false);
        });
    }

    public function down(): void
    {
        Schema::table('database_servers', function (Blueprint $table) {
            $table->dropConstrainedForeignId('moving_to_server_id');
        });
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn('mesh_moves'));
    }
};
