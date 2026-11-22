<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * FR-206: the WordPress sites. A website has one WordPress site at most, in its document root.
 * The panel keeps the versions that the agent reports, the update settings, the last update
 * and the restore point of the last update. The WP-CLI commands of a website and their output
 * are in "wordpress_commands".
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('wordpress_sites', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->unique()->constrained()->cascadeOnDelete();
            // The MariaDB database of the site, when the panel knows it. The restore point of an
            // update has a dump of it.
            $table->foreignUlid('database_id')->nullable()->constrained()->nullOnDelete();
            // "installing", "checking" (the panel looks for WordPress), "ready" or "failed".
            $table->string('status')->default('installing');
            $table->string('version', 40)->nullable();
            // The available updates of WordPress: the latest minor version and the latest version.
            $table->string('core_minor', 40)->nullable();
            $table->string('core_latest', 40)->nullable();
            $table->json('plugins')->nullable();
            $table->json('themes')->nullable();
            $table->string('site_url', 300)->nullable();
            $table->string('admin_user', 60)->nullable();
            $table->timestamp('checked_at')->nullable();
            // The last check in the maintenance window of the server.
            $table->timestamp('auto_checked_at')->nullable();
            // The automatic updates: "off", "minor" or "all" for WordPress, and on or off for the
            // plugins and the themes.
            $table->string('auto_update_core')->default('minor');
            $table->boolean('auto_update_plugins')->default(false);
            $table->boolean('auto_update_themes')->default(false);
            $table->json('last_update')->nullable();
            $table->json('restore_point')->nullable();
            $table->text('error')->nullable();
            // The install that waits for the new website. It has the passwords, encrypted.
            $table->text('pending_install')->nullable();
            $table->timestamps();
        });

        Schema::create('wordpress_commands', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            $table->ulid('agent_job_id')->nullable()->index();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            // The command with no passwords (WordPress::redact).
            $table->string('command', 2000);
            // "running", "done", or "failed" when the command did not start.
            $table->string('status')->default('running');
            $table->integer('exit_code')->nullable();
            $table->text('output')->nullable();
            $table->timestamp('ran_at')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('wordpress_commands');
        Schema::dropIfExists('wordpress_sites');
    }
};
