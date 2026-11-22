<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Account exports (docs/account-export.md): an archive with the description of an account, its
 * DNS zones, the files of its websites and the dumps of its databases. The panel keeps the
 * archive on its own disk for some days, and the users of the account download it.
 *
 * Each part of an export comes from the agent of a server: the files of one website or the
 * dump of one database. The panel writes the description and the zone files itself.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('account_exports', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->constrained()->cascadeOnDelete();
            // The user who started the export.
            $table->foreignId('user_id')->nullable()->constrained('users')->nullOnDelete();
            // "running", "ready" or "failed".
            $table->string('state')->default('running');
            // What the export has in addition to the description and the DNS zones.
            $table->boolean('files')->default(true);
            $table->boolean('databases')->default(true);
            // The description of the account (account.json) at the start of the export. It
            // has secrets, for example the environment variables of the websites, so the panel
            // keeps it encrypted.
            $table->longText('description');
            $table->unsignedBigInteger('size_bytes')->default(0);
            $table->string('error', 4000)->nullable();
            $table->timestamp('finished_at')->nullable();
            // The panel removes the archive at this time.
            $table->timestamp('expires_at')->nullable();
            $table->timestamps();
            $table->index(['customer_id', 'created_at']);
        });

        Schema::create('account_export_parts', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('account_export_id')->constrained()->cascadeOnDelete();
            // The job of the agent that sends the part, and its server.
            $table->foreignUlid('agent_job_id')->nullable()->constrained('agent_jobs')->nullOnDelete();
            $table->foreignUlid('server_id')->nullable()->constrained()->nullOnDelete();
            // The path of the part in the archive, for example "websites/shop.example.com/files.tar.gz".
            $table->string('path', 400);
            // The bytes that the panel has now. The agent sends the part in pieces.
            $table->unsignedBigInteger('received_bytes')->default(0);
            // The size and the SHA-256 hash in the result of the job. The part is complete when
            // they agree with the bytes that the panel has.
            $table->unsignedBigInteger('size_bytes')->nullable();
            $table->string('sha256', 64)->nullable();
            $table->timestamps();
            $table->unique(['account_export_id', 'path']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('account_export_parts');
        Schema::dropIfExists('account_exports');
    }
};
