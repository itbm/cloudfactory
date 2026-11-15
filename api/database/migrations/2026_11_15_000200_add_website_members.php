<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * A website is a network member that answers: a different website in a network with it can
 * connect to it on port 80, with the name "<name>.internal". As for a database server, the
 * website has one address of the mesh range, and the name and the address are the same on
 * each server.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('websites', function (Blueprint $table) {
            // The address of the website in the address range of the mesh. The panel gives the
            // address when a different website is in a network with this website.
            $table->string('mesh_address', 45)->nullable()->unique();
        });

        Schema::table('servers', function (Blueprint $table) {
            // True when the agent reported that a website on it can be a member that answers.
            $table->boolean('mesh_websites')->default(false);
        });
    }

    public function down(): void
    {
        Schema::table('websites', function (Blueprint $table) {
            $table->dropUnique(['mesh_address']);
            $table->dropColumn('mesh_address');
        });
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn('mesh_websites'));
    }
};
