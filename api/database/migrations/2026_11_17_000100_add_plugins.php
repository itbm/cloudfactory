<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Plugins. A plugin is a program outside the panel. It uses the public API with its own token
 * and gets the events of the panel by signed webhooks. The panel keeps its manifest, the key
 * that signed the manifest, the address of its webhook and its deliveries.
 *
 * Each plugin has its own user. The token of the plugin belongs to this user, and has only the
 * scopes of the manifest. Nobody can sign in as this user.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('plugin_keys', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('name', 100);
            $table->text('public_key');
            $table->string('fingerprint', 64)->unique();
            $table->timestamps();
        });

        Schema::create('plugins', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('slug', 40)->unique();
            $table->string('name', 100);
            $table->string('version', 40);
            $table->string('publisher', 100);
            $table->text('manifest');
            $table->string('signature', 512)->nullable();
            $table->foreignUlid('plugin_key_id')->nullable()->constrained('plugin_keys')->nullOnDelete();
            $table->boolean('enabled')->default(true);
            // The address where the administrator runs the plugin. The paths of the webhook
            // and the pages in the manifest are relative to it.
            $table->string('base_url', 255);
            $table->text('webhook_secret');
            $table->foreignId('user_id')->nullable()->constrained('users')->nullOnDelete();
            $table->timestamps();
        });

        Schema::create('plugin_deliveries', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('plugin_id')->constrained('plugins')->cascadeOnDelete();
            $table->string('event', 60);
            $table->text('payload');
            $table->string('state', 20)->default('pending');
            $table->unsignedSmallInteger('attempts')->default(0);
            $table->unsignedSmallInteger('response_status')->nullable();
            $table->string('error', 500)->nullable();
            $table->timestamp('delivered_at')->nullable();
            $table->timestamps();
            $table->index(['plugin_id', 'created_at']);
        });

        Schema::table('users', function (Blueprint $table) {
            $table->string('plugin_id', 26)->nullable()->index();
        });
    }

    public function down(): void
    {
        Schema::table('users', fn (Blueprint $table) => $table->dropColumn('plugin_id'));
        Schema::dropIfExists('plugin_deliveries');
        Schema::dropIfExists('plugins');
        Schema::dropIfExists('plugin_keys');
    }
};
