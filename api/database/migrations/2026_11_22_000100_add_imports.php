<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The import of an account from a different hosting panel, for example cPanel or DirectAdmin
 * (docs/imports.md).
 *
 * - "source" is "upload" (the panel keeps the archive in its storage) or "url" (each server
 *   downloads the archive from the URL). The model keeps the URL encrypted, because a URL
 *   of object storage can have a signature.
 * - "server_id" is the web server of the new websites. It downloads and reads the archive
 *   first. "servers" has each server that has the archive, or downloads it. When the import
 *   ends, the panel removes the archives, and "servers" is empty.
 * - "description" is the account in the format "cf-account/1", from the agent. The model
 *   keeps it encrypted: it has the password hashes of the mailboxes.
 * - "items" are the parts of the import and their state. The model keeps them encrypted: an
 *   item can hold a new database password until the panel sends it to the agent.
 * - Each job of an import has the import ("import_id" of agent_jobs). The panel continues the
 *   import when a job ends.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('imports', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->constrained()->cascadeOnDelete();
            $table->foreignUlid('server_id')->constrained()->cascadeOnDelete();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            $table->string('source');
            $table->text('url')->nullable();
            $table->string('file_name')->nullable();
            $table->unsignedBigInteger('size_bytes')->default(0);
            $table->unsignedBigInteger('received_bytes')->default(0);
            $table->string('sha256', 64)->nullable();
            // "uploading", "fetching", "ready", "starting", "running", "done" or "failed".
            $table->string('status');
            $table->string('panel')->nullable();
            $table->string('account')->nullable();
            $table->longText('description')->nullable();
            $table->longText('items')->nullable();
            $table->json('servers')->nullable();
            $table->text('error')->nullable();
            $table->timestamp('started_at')->nullable();
            $table->timestamp('finished_at')->nullable();
            $table->timestamps();
        });
        Schema::table('agent_jobs', function (Blueprint $table) {
            $table->foreignUlid('import_id')->nullable()->index();
        });
    }

    public function down(): void
    {
        Schema::table('agent_jobs', function (Blueprint $table) {
            $table->dropColumn('import_id');
        });
        Schema::dropIfExists('imports');
    }
};
