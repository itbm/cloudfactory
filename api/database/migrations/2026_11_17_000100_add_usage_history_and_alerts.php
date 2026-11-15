<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Usage history and usage alerts.
 *
 * - "usage_samples" keeps the resource use of each website, server and database server in
 *   buckets of 5 minutes (for 8 days) and of 1 hour (for 400 days). Each report of an agent
 *   adds to the bucket of its time. For a server, the memory and the disk values are percents.
 * - "usage_alerts" has one row for each time that a resource of a website, a server or a
 *   database server went near its limit. An alert is open until the use goes down again.
 * - A website keeps its traffic of the current calendar month, for the bandwidth limit.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('usage_samples', function (Blueprint $table) {
            $table->id();
            // "website", "server" or "database_server".
            $table->string('subject_type', 20);
            $table->string('subject_id', 26);
            // The length of the bucket in minutes: 5 or 60.
            $table->unsignedSmallInteger('resolution');
            $table->timestamp('bucket_at');
            // The number of reports in the bucket. The averages are the sums divided by it.
            $table->unsignedInteger('samples')->default(0);
            $table->double('cpu_sum')->default(0);
            $table->double('cpu_max')->default(0);
            $table->double('memory_sum')->default(0);
            $table->double('memory_max')->default(0);
            $table->double('disk')->default(0);
            $table->unsignedInteger('processes_max')->default(0);
            $table->unsignedBigInteger('bytes_in')->default(0);
            $table->unsignedBigInteger('bytes_out')->default(0);
            $table->unique(['subject_type', 'subject_id', 'resolution', 'bucket_at']);
            $table->index(['resolution', 'bucket_at']);
        });

        Schema::create('usage_alerts', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('subject_type', 20);
            $table->string('subject_id', 26);
            // The customer of the website or of the database server. A server has none.
            $table->foreignUlid('customer_id')->nullable()->constrained()->cascadeOnDelete();
            $table->foreignUlid('server_id')->nullable()->constrained()->nullOnDelete();
            // "cpu", "memory", "disk", "bandwidth" or "processes".
            $table->string('resource', 20);
            // "warning" or "critical".
            $table->string('level', 10);
            $table->double('used');
            $table->double('limit');
            $table->double('percent');
            // The highest level that the panel told the users about, for the delivery hooks.
            $table->string('notified_level', 10)->nullable();
            $table->timestamp('opened_at');
            $table->timestamp('resolved_at')->nullable();
            $table->timestamps();
            $table->index(['subject_type', 'subject_id', 'resolved_at']);
            $table->index(['customer_id', 'resolved_at']);
        });

        Schema::table('websites', function (Blueprint $table) {
            // The calendar month of the traffic, as "2026-11", and its bytes in and out.
            $table->string('bandwidth_month', 7)->nullable();
            $table->unsignedBigInteger('bandwidth_bytes')->default(0);
        });
    }

    public function down(): void
    {
        Schema::table('websites', fn (Blueprint $table) => $table->dropColumn(['bandwidth_month', 'bandwidth_bytes']));
        Schema::dropIfExists('usage_alerts');
        Schema::dropIfExists('usage_samples');
    }
};
