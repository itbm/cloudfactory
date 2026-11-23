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
            // "manual"; "webhook": a signed request to the webhook address starts a deploy; or
            // "schedule": the panel checks the branch each "check_minutes" minutes (FR-1004).
            $table->string('trigger')->default('manual');
            $table->unsignedSmallInteger('check_minutes')->default(5);
            // A push came while a deploy was in progress. The panel starts a deploy when the
            // deploy in progress is complete, unless that deploy got the commit of the push.
            $table->boolean('deploy_queued')->default(false);
            $table->string('queued_commit', 64)->nullable();
            // The last check of the branch on a schedule, and its error.
            $table->timestamp('checked_at')->nullable();
            $table->text('check_error')->nullable();
            $table->json('build_commands');
            // A command that changes the database, for example "php artisan migrate --force".
            // After a deploy with it, the panel does not go back automatically (FR-1009).
            $table->string('migration_command', 1000)->nullable();
            // The SSH key of the website for a private repository. The private key, the webhook
            // token, the webhook secret and the secrets are encrypted.
            $table->text('public_key');
            $table->text('private_key');
            $table->text('webhook_token');
            $table->string('webhook_token_hash', 64)->unique();
            // The Git host signs each webhook request with this secret.
            $table->text('webhook_secret');
            $table->text('secrets')->nullable();
            $table->timestamps();
        });

        Schema::table('releases', function (Blueprint $table) {
            // The ID of the Git commit. The column "ref" has the branch.
            $table->string('commit', 64)->nullable();
            // The document root of the website when it serves this release.
            $table->string('document_root')->nullable();
            // "manual", "webhook" or "schedule".
            $table->string('trigger')->default('manual');
            $table->ulid('agent_job_id')->nullable();
            $table->text('error')->nullable();
            // The error of the health check after the switch, for a deploy that ran a database
            // migration. The website keeps the release, and the owner gets an alert (FR-1009).
            $table->text('health_error')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('releases', fn (Blueprint $table) => $table->dropColumn(['commit', 'document_root', 'trigger', 'agent_job_id', 'error', 'health_error']));
        Schema::dropIfExists('git_sources');
        Schema::dropIfExists('cron_jobs');
    }
};
