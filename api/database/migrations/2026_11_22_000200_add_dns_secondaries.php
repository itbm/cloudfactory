<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Secondary DNS: DNS servers outside the panel that copy the zones from the DNS servers of the
 * panel with zone transfers (AXFR and IXFR). The panel never copies a zone from a different
 * server.
 *
 * - A secondary of the panel ("dns_secondaries") copies all zones. An administrator adds it.
 *   "addresses" are the addresses that can ask for a transfer, and that get NOTIFY when
 *   "notify" is true. With "name_server", each zone of the panel gets an NS record for the
 *   secondary. With "catalog", the secondary also copies the catalog zone (RFC 9432), which
 *   lists the zones, so that it finds a new zone itself.
 * - The owner of a zone can give its own secondaries for the zone ("transfer_*" of a zone).
 *   Their addresses must be public. "transfer_name_servers" are more NS records of the zone.
 * - A TSIG key signs the transfers. The model keeps each secret encrypted. The key of the
 *   transfers of a zone has the name "transfer." and the name of the zone.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('dns_secondaries', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('name', 60);
            $table->json('addresses');
            $table->string('name_server', 253)->nullable();
            $table->string('tsig_name', 253)->nullable()->unique();
            $table->string('tsig_algorithm', 20)->nullable();
            $table->text('tsig_secret')->nullable();
            $table->boolean('notify')->default(true);
            $table->boolean('catalog')->default(false);
            $table->timestamps();
        });
        Schema::table('dns_zones', function (Blueprint $table) {
            $table->json('transfer_addresses')->nullable();
            $table->boolean('transfer_notify')->default(false);
            $table->string('transfer_tsig_algorithm', 20)->nullable();
            $table->text('transfer_tsig_secret')->nullable();
            $table->json('transfer_name_servers')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('dns_zones', function (Blueprint $table) {
            $table->dropColumn(['transfer_addresses', 'transfer_notify', 'transfer_tsig_algorithm', 'transfer_tsig_secret', 'transfer_name_servers']);
        });
        Schema::dropIfExists('dns_secondaries');
    }
};
