<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The sessions of the web terminal of a website (FR-802). The panel adds a row when a user
 * opens the terminal. The agent adds each command of the session to the command log of the
 * website, and at the end it sends the reason and the recording of the output.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('terminal_sessions', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            $table->ulid('customer_id')->nullable()->index();
            // Who opened the terminal, as in the audit log.
            $table->unsignedBigInteger('user_id')->nullable();
            $table->string('actor');
            $table->string('ip', 45)->nullable();
            // "opening" (the agent connects), "open", "ended" or "failed" (it did not open).
            $table->string('state', 16);
            $table->unsignedSmallInteger('cols');
            $table->unsignedSmallInteger('rows');
            $table->ulid('agent_job_id')->nullable()->index();
            // The text that the browser showed at the end, and the exit code of the shell.
            $table->string('end_reason', 500)->nullable();
            $table->integer('exit_code')->nullable();
            $table->unsignedInteger('command_count')->default(0);
            // The output of the terminal in the asciicast v2 format, at most 1 MiB.
            $table->longText('recording')->nullable();
            $table->boolean('recording_cut')->default(false);
            $table->timestamp('started_at')->nullable();
            $table->timestamp('ended_at')->nullable();
            $table->timestamps();
            $table->index(['website_id', 'created_at']);
        });
        // A command of the command log that a user typed in the web terminal.
        Schema::table('website_commands', function (Blueprint $table) {
            $table->foreignUlid('terminal_session_id')->nullable()->after('source')->constrained()->nullOnDelete();
        });
    }

    public function down(): void
    {
        Schema::table('website_commands', function (Blueprint $table) {
            $table->dropConstrainedForeignId('terminal_session_id');
        });
        Schema::dropIfExists('terminal_sessions');
    }
};
