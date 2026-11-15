<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The cron jobs of a website, and the deploys of a website from a Git repository.
 *
 * The table "releases" is from the first release of the panel, and no code used it. A release
 * is now the result of one deploy: a directory "releases/<id>" in the files of the website.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('cron_jobs', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            $table->string('schedule', 200);
            $table->string('command', 1000);
            $table->boolean('enabled')->default(true);
            // The last run that the agent reported.
            $table->timestamp('last_run_at')->nullable();
            $table->integer('last_exit_code')->nullable();
            $table->text('last_output')->nullable();
            $table->timestamps();
        });

        Schema::create('git_sources', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->unique()->constrained()->cascadeOnDelete();
            $table->string('repository', 300);
            $table->string('branch', 100);
            // The directory in the repository that the web server serves. "." is the repository itself.
            $table->string('web_directory')->default('public');
            // "manual", or "webhook": a request to the webhook address starts a deploy.
            $table->string('trigger')->default('manual');
            $table->json('build_commands');
            // The SSH key of the website for a private repository. The private key, the webhook
            // token and the secrets are encrypted.
            $table->text('public_key');
            $table->text('private_key');
            $table->text('webhook_token');
            $table->string('webhook_token_hash', 64)->unique();
            $table->text('secrets')->nullable();
            $table->timestamps();
        });

        Schema::table('releases', function (Blueprint $table) {
            // The ID of the Git commit. The column "ref" has the branch.
            $table->string('commit', 64)->nullable();
            // The document root of the website when it serves this release.
            $table->string('document_root')->nullable();
            // "manual" or "webhook".
            $table->string('trigger')->default('manual');
            $table->ulid('agent_job_id')->nullable();
            $table->text('error')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('releases', fn (Blueprint $table) => $table->dropColumn(['commit', 'document_root', 'trigger', 'agent_job_id', 'error']));
        Schema::dropIfExists('git_sources');
        Schema::dropIfExists('cron_jobs');
    }
};
