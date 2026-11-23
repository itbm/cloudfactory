<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The command log of a website: each command that a user ran in the container of the website
 * (FR-802). The panel action "Run a command" adds a row before it sends the job, and the result
 * of the job completes the row.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('website_commands', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            // The customer of the website, for the lists of a reseller.
            $table->ulid('customer_id')->nullable()->index();
            // Who ran the command, as in the audit log: the user, the name in the log and the
            // address of the client.
            $table->unsignedBigInteger('user_id')->nullable();
            $table->string('actor');
            $table->string('ip', 45)->nullable();
            // Where the command came from: "run" is the action "Run a command" of the panel.
            $table->string('source', 16);
            $table->text('command');
            // "running", "done" (the command ran, with an exit code) or "failed" (it did not start).
            $table->string('state', 16);
            $table->integer('exit_code')->nullable();
            $table->boolean('timed_out')->default(false);
            $table->unsignedInteger('timeout_seconds')->nullable();
            // The end of the output: the standard output and the standard error.
            $table->text('output')->nullable();
            $table->ulid('agent_job_id')->nullable()->index();
            $table->timestamp('started_at')->nullable();
            $table->timestamp('finished_at')->nullable();
            $table->timestamps();
            $table->index(['website_id', 'created_at']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('website_commands');
    }
};
