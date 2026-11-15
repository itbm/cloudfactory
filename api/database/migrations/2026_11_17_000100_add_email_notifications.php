<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Email notifications. The panel sends an email when an event occurs, for example a failed job
 * or a certificate that expires soon. Each user chooses the events that they receive.
 *
 * - mail_servers: the SMTP server that sends the emails. The row with no customer is the server
 *   of the panel. A reseller can have its own server, so that its customers get emails from
 *   the brand of the reseller.
 * - notification_templates: a changed subject and text of an event. The row with no customer
 *   is for the panel. A reseller can change the templates for its customers.
 * - notification_deliveries: each email that the panel sent, or tried to send.
 * - user_devices: the devices that a user signed in from. A sign-in from a different device
 *   gives an email.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('mail_servers', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->nullable()->unique()->constrained()->cascadeOnDelete();
            $table->boolean('enabled')->default(true);
            $table->string('host');
            $table->unsignedInteger('port');
            // tls: TLS from the start (port 465). starttls: STARTTLS is necessary. none: no TLS.
            $table->string('encryption', 10);
            $table->string('username')->nullable();
            $table->text('password')->nullable();
            $table->string('from_address');
            $table->string('from_name')->nullable();
            $table->string('reply_to')->nullable();
            $table->timestamp('tested_at')->nullable();
            $table->text('test_error')->nullable();
            $table->timestamps();
        });

        Schema::create('notification_templates', function (Blueprint $table) {
            $table->id();
            $table->foreignUlid('customer_id')->nullable()->constrained()->cascadeOnDelete();
            $table->string('event', 40);
            $table->string('subject', 200);
            $table->text('body');
            $table->timestamps();
            $table->unique(['customer_id', 'event']);
        });

        Schema::create('notification_deliveries', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            // The customer of the user. A reseller sees the emails of its customers.
            $table->foreignUlid('customer_id')->nullable()->constrained()->nullOnDelete();
            // The reseller whose SMTP server sent the email. Null: the server of the panel.
            $table->foreignUlid('sender_id')->nullable()->constrained('customers')->nullOnDelete();
            $table->string('event', 40);
            $table->string('recipient');
            $table->string('subject', 300);
            $table->text('body');
            // queued, sent, failed or skipped (no mail server).
            $table->string('state', 10);
            $table->text('error')->nullable();
            $table->unsignedSmallInteger('attempts')->default(0);
            // One email for each event and user: for example the same certificate on the same day.
            $table->string('dedupe_key')->nullable();
            $table->timestamp('sent_at')->nullable();
            $table->timestamps();
            $table->unique(['user_id', 'dedupe_key']);
            $table->index(['created_at']);
        });

        Schema::create('user_devices', function (Blueprint $table) {
            $table->id();
            $table->foreignId('user_id')->constrained()->cascadeOnDelete();
            // SHA-256 of the device ID that the client sends, or of the user agent.
            $table->string('fingerprint', 64);
            $table->string('label', 200);
            $table->string('ip', 45)->nullable();
            $table->timestamp('last_seen_at');
            $table->timestamps();
            $table->unique(['user_id', 'fingerprint']);
        });

        Schema::table('users', function (Blueprint $table) {
            // The events that the user changed from the default: {"job.failed": false, ...}.
            $table->json('notification_preferences')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('users', fn (Blueprint $table) => $table->dropColumn('notification_preferences'));
        Schema::dropIfExists('user_devices');
        Schema::dropIfExists('notification_deliveries');
        Schema::dropIfExists('notification_templates');
        Schema::dropIfExists('mail_servers');
    }
};
