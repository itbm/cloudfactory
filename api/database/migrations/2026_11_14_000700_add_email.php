<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Email hosting. A server with the role "email" has one mail server (Stalwart) in a container.
 * A customer adds mail domains, and each mail domain has mailboxes, aliases and forwarders.
 *
 * - "mail_hostname" of a server is the name that mail programs and other mail servers connect
 *   to. "mail_status" is null until an administrator turns email on.
 * - "mail_settings" of a server has the spam filter and the outbound relay of its mail server
 *   (App\Services\MailSettings). Null: the default settings. The panel keeps the password of
 *   the relay encrypted in "mail_relay_password".
 * - A plan gives the number of mailboxes of a customer ("mailboxes", from the first release)
 *   and the total size of these mailboxes ("mail_storage_bytes").
 * - The panel keeps no mailbox password. It sends only a hash to the agent.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('servers', function (Blueprint $table) {
            $table->string('mail_hostname')->nullable();
            $table->string('mail_status')->nullable();
            $table->json('mail_settings')->nullable();
            $table->text('mail_relay_password')->nullable();
        });
        Schema::table('plans', function (Blueprint $table) {
            $table->unsignedBigInteger('mail_storage_bytes')->default(10 * 1024 ** 3);
        });
        Schema::create('mail_domains', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->constrained();
            $table->foreignUlid('server_id')->constrained();
            $table->string('name')->unique();
            $table->string('status')->default('pending');
            // The DNS records that the customer must add: "type", "name", "value", "purpose"
            // and "found" (null before the first check).
            $table->json('records')->nullable();
            $table->timestamp('dns_checked_at')->nullable();
            $table->timestamps();
        });
        Schema::create('mailboxes', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->constrained();
            $table->foreignUlid('mail_domain_id')->constrained();
            $table->string('local_part');
            $table->unsignedBigInteger('quota_bytes');
            // The last report of the agent.
            $table->unsignedBigInteger('used_bytes')->default(0);
            $table->string('status')->default('pending');
            $table->timestamps();
            $table->unique(['mail_domain_id', 'local_part']);
        });
        Schema::create('mail_aliases', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->constrained();
            $table->foreignUlid('mail_domain_id')->constrained();
            $table->string('local_part');
            // "alias": a second address of one mailbox. "forwarder": a list of addresses.
            $table->string('kind');
            $table->json('destinations');
            $table->string('status')->default('pending');
            $table->timestamps();
            $table->unique(['mail_domain_id', 'local_part']);
        });
        // The job of a mail domain, a mailbox or an alias has no website. The users of the
        // customer can see the job.
        Schema::table('agent_jobs', function (Blueprint $table) {
            $table->ulid('customer_id')->nullable()->index();
        });
    }

    public function down(): void
    {
        // SQLite needs the index removed before the column.
        Schema::table('agent_jobs', fn (Blueprint $table) => $table->dropIndex(['customer_id']));
        Schema::table('agent_jobs', fn (Blueprint $table) => $table->dropColumn('customer_id'));
        Schema::dropIfExists('mail_aliases');
        Schema::dropIfExists('mailboxes');
        Schema::dropIfExists('mail_domains');
        Schema::table('plans', fn (Blueprint $table) => $table->dropColumn('mail_storage_bytes'));
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn(['mail_hostname', 'mail_status', 'mail_settings', 'mail_relay_password']));
    }
};
