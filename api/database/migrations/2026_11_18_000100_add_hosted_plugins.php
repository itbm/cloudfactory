<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Plugins that the panel runs: a container of the signed image of the manifest, in a gVisor
 * sandbox on a server of the panel (App\Services\Plugins\Hosting).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('plugins', function (Blueprint $table) {
            $table->foreignUlid('server_id')->nullable()->after('base_url')->constrained()->nullOnDelete();
            $table->string('unix_user', 32)->nullable()->unique()->after('server_id');
            $table->string('host')->nullable()->after('unix_user');
            // starting, running, stopped, failed or removing. Null for a plugin outside the panel.
            $table->string('container_status', 16)->nullable()->after('host');
            // The token of the plugin, encrypted. The panel gives it to the container again when it
            // makes the container again. A plugin outside the panel has none.
            $table->text('hosted_token')->nullable()->after('container_status');
        });
    }

    public function down(): void
    {
        Schema::table('plugins', function (Blueprint $table) {
            $table->dropConstrainedForeignId('server_id');
            $table->dropUnique(['unix_user']);
            $table->dropColumn(['unix_user', 'host', 'container_status', 'hosted_token']);
        });
    }
};
