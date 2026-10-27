<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Changes for the completeness audit of release 0.1: SFTP, the document root, custom images,
 * suspension and the check of old jobs.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('websites', function (Blueprint $table) {
            // Batch 1, finding 11: the directory in /app that the web server serves.
            $table->string('document_root')->default('public');
            // Spec section 10: SFTP access for the files of the website.
            $table->boolean('sftp_enabled')->default(false);
            $table->json('sftp_keys')->nullable();
            $table->boolean('sftp_password_set')->default(false);
            // A suspension stopped the website. Only these websites start when it ends.
            $table->boolean('stopped_by_suspension')->default(false);
        });
        Schema::table('images', function (Blueprint $table) {
            // FR-307: the HTTP port and the start command of a custom image.
            $table->unsignedInteger('container_port')->nullable();
            $table->json('command')->nullable();
        });
        Schema::table('agent_jobs', function (Blueprint $table) {
            // Batch 1, finding 2: the panel sends a job again when the agent does not report.
            $table->timestamp('resent_at')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('agent_jobs', fn (Blueprint $table) => $table->dropColumn('resent_at'));
        Schema::table('images', fn (Blueprint $table) => $table->dropColumn(['container_port', 'command']));
        Schema::table('websites', fn (Blueprint $table) => $table->dropColumn(['document_root', 'sftp_enabled', 'sftp_keys', 'sftp_password_set', 'stopped_by_suspension']));
    }
};
