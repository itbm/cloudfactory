<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * An image of a runtime from a manifest of the agent (runtimeManifests in agent.json), for
 * example Go. The image runs the start command of the website, as a Node.js or Python image
 * does. Its family is the name of the runtime. The existing images are not runtime images
 * from a manifest, so they do not change.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('images', function (Blueprint $table) {
            $table->boolean('app_runtime')->default(false);
            $table->string('default_command', 1000)->nullable();
            $table->string('default_install', 1000)->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('images', function (Blueprint $table) {
            $table->dropColumn(['app_runtime', 'default_command', 'default_install']);
        });
    }
};
