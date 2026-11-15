<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;
use Illuminate\Support\Str;

/**
 * The private networks of the customers.
 *
 * A network has members: websites and database servers of one customer. A website can connect
 * to each database server that is in a network with it, on the same server or on a different
 * server. A database server has a name ("<name>.internal") and an address of the mesh range.
 * The name and the address are the same on each server.
 *
 * Each customer has the network "default". Each website and each database server of the
 * customer is a member of it.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('networks', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->constrained()->cascadeOnDelete();
            $table->string('name', 40);
            // True for the network that each website and each database server of the customer
            // joins when the panel makes it.
            $table->boolean('is_default')->default(false);
            $table->timestamps();
            $table->unique(['customer_id', 'name']);
        });

        Schema::create('network_members', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('network_id')->constrained()->cascadeOnDelete();
            // A member is one website or one database server.
            $table->foreignUlid('website_id')->nullable()->constrained()->cascadeOnDelete();
            $table->foreignUlid('database_server_id')->nullable()->constrained()->cascadeOnDelete();
            $table->timestamps();
            $table->unique(['network_id', 'website_id']);
            $table->unique(['network_id', 'database_server_id']);
        });

        Schema::table('database_servers', function (Blueprint $table) {
            // The address of the database server in the address range of the mesh. The name
            // "<name>.internal" points to it on each server.
            $table->string('mesh_address', 45)->nullable()->unique();
        });

        Schema::table('servers', function (Blueprint $table) {
            // True when the agent reported that it can operate network members.
            $table->boolean('mesh_networks')->default(false);
            // The hash of the mesh state that the panel sent last. The panel sends a new
            // state only when the state is different.
            $table->string('mesh_state_hash', 64)->nullable();
        });

        // The network "default" of each customer that has a website or a database server,
        // with each of them as a member.
        $now = now();
        $customers = DB::table('websites')->select('customer_id')->union(DB::table('database_servers')->select('customer_id'))->pluck('customer_id')->unique();
        foreach ($customers as $customerId) {
            $networkId = (string) Str::ulid();
            DB::table('networks')->insert(['id' => $networkId, 'customer_id' => $customerId, 'name' => 'default', 'is_default' => true, 'created_at' => $now, 'updated_at' => $now]);
            foreach (['websites' => 'website_id', 'database_servers' => 'database_server_id'] as $table => $column) {
                foreach (DB::table($table)->where('customer_id', $customerId)->pluck('id') as $id) {
                    DB::table('network_members')->insert(['id' => (string) Str::ulid(), 'network_id' => $networkId, $column => $id, 'created_at' => $now, 'updated_at' => $now]);
                }
            }
        }
    }

    public function down(): void
    {
        // SQLite needs the index removed before the column.
        Schema::table('database_servers', fn (Blueprint $table) => $table->dropUnique(['mesh_address']));
        Schema::table('database_servers', fn (Blueprint $table) => $table->dropColumn('mesh_address'));
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn(['mesh_networks', 'mesh_state_hash']));
        Schema::dropIfExists('network_members');
        Schema::dropIfExists('networks');
    }
};
