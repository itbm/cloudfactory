<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Node.js and Python websites, and the environment variables of each website.
 *
 * - images.runtime_version: the version of Node.js or Python of an official image, for
 *   example "24" or "3.14". A PHP image keeps its version in php_version.
 * - websites.command: also the start command of a Node.js or Python website, for example
 *   "npm start". Before, only a worker had one.
 * - websites.install_command: the command that installs the dependencies of a Node.js or
 *   Python website, for example "npm ci --omit=dev". Empty means no install step.
 * - websites.environment: the environment variables of the website container, encrypted.
 *   The panel sends them to the agent with each job, and the API never gives the values.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('images', function (Blueprint $table) {
            $table->string('runtime_version', 16)->nullable();
        });
        Schema::table('websites', function (Blueprint $table) {
            $table->text('install_command')->nullable();
            $table->text('environment')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('images', fn (Blueprint $table) => $table->dropColumn('runtime_version'));
        Schema::table('websites', fn (Blueprint $table) => $table->dropColumn(['install_command', 'environment']));
    }
};
