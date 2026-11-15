<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * A mail cluster: mail servers on three or five servers that share their data. The mail
 * service continues when one server fails.
 *
 * - "hostname" is the name that all members share. Its DNS records give the addresses of all
 *   members, and the MX record of each mail domain names it.
 * - "phase" is the step of the setup: "prepare" (each server makes its keys), "bootstrap" (the
 *   first server starts the database cluster), "join" (the other servers join it), "first"
 *   (the first mail server applies the settings), "rest" (the other mail servers start) and
 *   "done".
 * - "secrets" has the password of the database and the two secrets of the cluster store. The
 *   panel encrypts the column.
 * - "mail_cluster_member" of a server has its number, its addresses, its public keys and the
 *   step that it completed.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('mail_clusters', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('name')->unique();
            $table->string('hostname')->unique();
            $table->string('status');
            $table->string('phase');
            $table->string('network');
            $table->text('secrets');
            $table->text('error')->nullable();
            $table->timestamps();
        });
        Schema::table('servers', function (Blueprint $table) {
            $table->foreignUlid('mail_cluster_id')->nullable()->constrained();
            $table->json('mail_cluster_member')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('servers', function (Blueprint $table) {
            $table->dropConstrainedForeignId('mail_cluster_id');
            $table->dropColumn('mail_cluster_member');
        });
        Schema::dropIfExists('mail_clusters');
    }
};
