<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * DNS hosting. A customer has DNS zones, as it has websites. The panel keeps each zone and
 * its records, and sends the full zone to each server with the role "dns". Each of these
 * servers answers the queries for the zone: there is no transfer between the servers.
 *
 * - A plan gives the number of zones of a customer ("dns_zones").
 * - A server with the role "dns" has a host name as a name server ("dns_name"), for example
 *   "ns1.example.net". The agent reports that it can operate DNS ("dns") and the public
 *   addresses of the server ("addresses"). The panel uses the addresses for the A and AAAA
 *   records of the websites.
 * - "sync" of a zone has the result of the last job of each DNS server.
 * - "dnssec_key" is the private key of the zone. The model keeps it encrypted.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('plans', function (Blueprint $table) {
            $table->unsignedInteger('dns_zones')->default(5);
        });
        Schema::table('servers', function (Blueprint $table) {
            $table->string('dns_name')->nullable();
            $table->boolean('dns')->default(false);
            $table->json('addresses')->nullable();
        });
        Schema::create('dns_zones', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->constrained();
            $table->string('name')->unique();
            // The serial of the SOA record. Each change of the zone makes it larger.
            $table->unsignedBigInteger('serial');
            // "active", or "removing" until each DNS server removed the zone.
            $table->string('status')->default('active');
            // What the public DNS says about the zone: "here" (the name servers of the zone are
            // the name servers of the panel), "elsewhere", "none" (no NS records) or "unknown".
            $table->string('delegation')->default('unknown');
            $table->json('delegation_name_servers')->nullable();
            $table->timestamp('delegation_checked_at')->nullable();
            $table->boolean('dnssec')->default(false);
            $table->text('dnssec_key')->nullable();
            $table->unsignedInteger('dnssec_key_tag')->nullable();
            // The public key of the DNSKEY record, in Base64.
            $table->string('dnssec_public_key')->nullable();
            $table->json('sync')->nullable();
            $table->timestamps();
        });
        Schema::create('dns_records', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('dns_zone_id')->constrained()->cascadeOnDelete();
            // The name in the zone, without the name of the zone. "@" is the zone itself.
            $table->string('name');
            $table->string('type', 10);
            $table->unsignedInteger('ttl');
            $table->text('value');
            // Only for MX and SRV.
            $table->unsignedInteger('priority')->nullable();
            // A record that the panel made and keeps: "ns" (the name servers of the zone) or
            // "website" (the address of a website that has the name as a domain).
            $table->string('managed', 20)->nullable();
            $table->timestamps();
            $table->index(['dns_zone_id', 'name', 'type']);
        });
        // The job of one zone has no website. The owner of the zone can see the job.
        Schema::table('agent_jobs', function (Blueprint $table) {
            $table->ulid('dns_zone_id')->nullable()->index();
        });
    }

    public function down(): void
    {
        // SQLite needs the index removed before the column.
        Schema::table('agent_jobs', fn (Blueprint $table) => $table->dropIndex(['dns_zone_id']));
        Schema::table('agent_jobs', fn (Blueprint $table) => $table->dropColumn('dns_zone_id'));
        Schema::dropIfExists('dns_records');
        Schema::dropIfExists('dns_zones');
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn(['dns_name', 'dns', 'addresses']));
        Schema::table('plans', fn (Blueprint $table) => $table->dropColumn('dns_zones'));
    }
};
