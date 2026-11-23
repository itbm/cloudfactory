<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Uptime checks of websites (App\Services\Uptime\Monitor).
 *
 * - "uptime_checks" has one row for each address that the panel requests on a schedule: a
 *   domain of the website, a path, the interval and the answer that the check expects.
 * - "uptime_results" has the result of each request, for 8 days.
 * - "uptime_incidents" has one row for each time that a check was down: from the first failed
 *   request to the first request that succeeded again. The panel keeps a closed incident for
 *   90 days.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('uptime_checks', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            // A domain of the website. When the domain goes, its checks go too.
            $table->foreignId('domain_id')->constrained()->cascadeOnDelete();
            // "https" or "http".
            $table->string('scheme', 5);
            // The path and the query, for example "/health?full=1".
            $table->string('path', 255);
            $table->unsignedInteger('interval_seconds');
            $table->unsignedSmallInteger('timeout_seconds');
            // The HTTP status that the check expects. Null: each status from 200 to 399.
            $table->unsignedSmallInteger('expected_status')->nullable();
            // A text that the answer must have, or null.
            $table->string('keyword', 100)->nullable();
            // The number of failed requests in a row that make the check "down".
            $table->unsignedSmallInteger('failures_before_alert');
            $table->boolean('enabled')->default(true);
            // "pending" (no result yet), "up" or "down".
            $table->string('state', 10)->default('pending');
            // The failed requests in a row, and the time of the first one.
            $table->unsignedInteger('failures')->default(0);
            $table->timestamp('failing_since')->nullable();
            $table->timestamp('state_changed_at')->nullable();
            $table->timestamp('next_check_at')->nullable();
            $table->timestamp('last_checked_at')->nullable();
            $table->unsignedSmallInteger('last_status')->nullable();
            $table->unsignedInteger('last_response_ms')->nullable();
            $table->string('last_error', 300)->nullable();
            $table->timestamps();
            $table->unique(['website_id', 'domain_id', 'scheme', 'path']);
            $table->index(['enabled', 'next_check_at']);
            $table->index(['state']);
        });

        Schema::create('uptime_results', function (Blueprint $table) {
            $table->id();
            $table->foreignUlid('uptime_check_id')->constrained()->cascadeOnDelete();
            $table->timestamp('checked_at');
            $table->boolean('ok');
            $table->unsignedSmallInteger('status')->nullable();
            $table->unsignedInteger('response_ms')->nullable();
            $table->string('error', 300)->nullable();
            $table->index(['uptime_check_id', 'checked_at']);
            $table->index(['checked_at']);
        });

        Schema::create('uptime_incidents', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('uptime_check_id')->constrained()->cascadeOnDelete();
            // The time of the first failed request.
            $table->timestamp('started_at');
            // The time of the first request that succeeded again, or of the pause of the check.
            $table->timestamp('resolved_at')->nullable();
            // The error of the request that made the check "down".
            $table->string('error', 300);
            // True when the check was paused (turned off, or the website stopped) while it was down.
            $table->boolean('ended_by_pause')->default(false);
            $table->timestamps();
            $table->index(['uptime_check_id', 'started_at']);
            $table->index(['resolved_at']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('uptime_incidents');
        Schema::dropIfExists('uptime_results');
        Schema::dropIfExists('uptime_checks');
    }
};
