<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * External DNS providers. A plugin can offer a DNS provider, for example Cloudflare (contract
 * cf-dns-provider/1). A DNS account has the credentials for one provider. A zone with an
 * account goes to that provider, and not to the DNS servers of the panel. The panel keeps the
 * records, and the plugin makes the zone at the provider the same after each change.
 *
 * - An account of a customer ("customer_id") is only for the zones of that customer. An
 *   account with no customer belongs to the panel: an administrator makes it, and each
 *   customer can use it.
 * - "credentials" has the values of the fields of the manifest. The model keeps them encrypted.
 * - "account" is the name of the account at the provider, as the plugin gives it.
 * - "provider_state" of a zone has the result of the last change at the provider: the serial,
 *   the state, the error, the name servers that the provider gives the zone, and DNSSEC.
 * - "handover": the zone moved from the DNS servers of the panel to a provider. The DNS servers
 *   of the panel keep the zone until "handover_ends_at", so that resolvers that still have the
 *   old name servers get an answer. The time starts when the public DNS stops giving the name
 *   servers of the panel.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('dns_accounts', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->nullable()->constrained();
            $table->foreignUlid('plugin_id')->constrained();
            // The ID of the provider in the manifest of the plugin, for example "cloudflare".
            $table->string('provider', 40);
            $table->string('name', 60);
            $table->text('credentials');
            $table->string('account', 100)->nullable();
            $table->timestamp('checked_at')->nullable();
            $table->timestamps();
        });
        Schema::table('dns_zones', function (Blueprint $table) {
            $table->foreignUlid('dns_account_id')->nullable()->constrained();
            $table->json('provider_state')->nullable();
            $table->boolean('handover')->default(false);
            $table->timestamp('handover_ends_at')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('dns_zones', function (Blueprint $table) {
            $table->dropConstrainedForeignId('dns_account_id');
            $table->dropColumn(['provider_state', 'handover', 'handover_ends_at']);
        });
        Schema::dropIfExists('dns_accounts');
    }
};
