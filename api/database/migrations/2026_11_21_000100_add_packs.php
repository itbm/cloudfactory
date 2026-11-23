<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Packs (docs/packs.md): a signed manifest from a publisher that adds a website runtime, with
 * one image for each version of the runtime, or a database engine. The panel keeps the exact
 * bytes of the manifest that the publisher signed, because each agent checks the signature
 * again.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('packs', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('slug', 40)->unique();
            $table->string('name', 100);
            $table->string('version', 40);
            $table->string('publisher', 100);
            $table->string('description', 1000)->nullable();
            // "runtime" or "database" (the kind of the manifest).
            $table->string('kind', 20)->default('runtime');
            // The first host port of the block of a database pack: the database servers of its
            // engine get the ports from this port (DatabaseEngines::PORTS). It does not change
            // while the pack is installed. Null for a runtime pack.
            $table->unsignedInteger('database_port')->nullable()->unique();
            $table->text('manifest');
            // The signature of the manifest and the public key of the key that signed it. The
            // panel sends both to each agent. A pack from the developer mode has neither.
            $table->text('signature')->nullable();
            $table->text('public_key')->nullable();
            // The trusted key that signed the manifest. It is null for the release key of
            // Cloudfactory and for a pack with no signature.
            $table->foreignUlid('plugin_key_id')->nullable()->constrained('plugin_keys');
            $table->string('signer', 100)->nullable();
            $table->foreignId('user_id')->nullable()->constrained('users')->nullOnDelete();
            $table->timestamps();
        });

        Schema::table('images', function (Blueprint $table) {
            $table->foreignUlid('pack_id')->nullable()->constrained('packs');
        });

        Schema::table('servers', function (Blueprint $table) {
            // The packs that the agent reports: [{slug, version}].
            $table->json('agent_packs')->nullable();
            // The tries to send the packs again after a report with other packs.
            $table->json('packs_resync')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn(['agent_packs', 'packs_resync']));
        Schema::table('images', function (Blueprint $table) {
            $table->dropConstrainedForeignId('pack_id');
        });
        Schema::dropIfExists('packs');
    }
};
