<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The mesh: encrypted tunnels between the servers of the panel. A website on one server can
 * then use a database server on a different server, and the traffic between them is safe on
 * the public internet.
 *
 * The panel is the directory of the mesh. A server makes its own key pair and reports the
 * public key. The panel gives each server a mesh address, makes a secret key for each pair
 * of servers, and sends each server the list of its peers (the agent action SetMesh).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('servers', function (Blueprint $table) {
            // The public tunnel key that the agent reported. The private key stays on the server.
            $table->string('mesh_public_key', 64)->nullable();
            // The address of the server in the address range of the mesh. It does not change.
            $table->string('mesh_address', 45)->nullable()->unique();
            // The public IP address that the other servers send the tunnel traffic to.
            $table->string('mesh_endpoint', 45)->nullable();
            // The version of the mesh state that the agent reported, and the version that the
            // panel wants the server to have.
            $table->unsignedBigInteger('mesh_version')->default(0);
            $table->unsignedBigInteger('mesh_wanted_version')->default(0);
        });

        Schema::create('mesh_tunnels', function (Blueprint $table) {
            $table->ulid('id')->primary();
            // The two servers. "server_a_id" is the smaller ID, so a pair has one record.
            $table->foreignUlid('server_a_id')->constrained('servers')->cascadeOnDelete();
            $table->foreignUlid('server_b_id')->constrained('servers')->cascadeOnDelete();
            // The secret key of the pair, encrypted with the application key.
            $table->text('preshared_key');
            // True for a tunnel that an administrator made by hand.
            $table->boolean('pinned')->default(false);
            // What each of the two servers reported last. The key is the ID of the server.
            $table->json('status')->nullable();
            $table->timestamps();
            $table->unique(['server_a_id', 'server_b_id']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('mesh_tunnels');
        // SQLite needs the index removed before the column.
        Schema::table('servers', fn (Blueprint $table) => $table->dropUnique(['mesh_address']));
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn(['mesh_public_key', 'mesh_address', 'mesh_endpoint', 'mesh_version', 'mesh_wanted_version']));
    }
};
