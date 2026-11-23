<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Certificates from the panel (docs/certificates.md): a certificate that a customer uploads,
 * or a certificate that the panel gets from an ACME CA with DNS validation (DNS-01), for
 * example a wildcard certificate. The panel sends each certificate to the edge proxy of each
 * server with a website domain that uses it.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('certificates', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->constrained('customers')->cascadeOnDelete();
            // "upload" or "acme".
            $table->string('source', 10);
            // The names of the certificate, in lower case. For an ACME certificate, the names
            // of the order. For an uploaded certificate, the DNS names of the certificate.
            $table->json('names');
            // The current certificate: the chain in PEM format and its private key. An ACME
            // certificate has none until its first order is complete.
            $table->text('certificate')->nullable();
            $table->text('private_key')->nullable();
            $table->string('fingerprint', 64)->nullable();
            $table->string('issuer', 255)->nullable();
            $table->timestamp('not_before')->nullable();
            $table->timestamp('expires_at')->nullable();
            // True when the chain goes to a root certificate that browsers trust.
            $table->boolean('trusted')->default(false);
            // An ACME order that runs: its state, and the private key of the new certificate.
            $table->json('order')->nullable();
            $table->text('order_key')->nullable();
            // The last error of an ACME order, and the time of the next try.
            $table->text('error')->nullable();
            $table->timestamp('next_try_at')->nullable();
            $table->unsignedInteger('tries')->default(0);
            $table->timestamp('renew_at')->nullable();
            $table->foreignId('user_id')->nullable()->constrained('users')->nullOnDelete();
            $table->timestamps();
            $table->index(['customer_id', 'expires_at']);
        });

        Schema::table('servers', function (Blueprint $table) {
            // The certificates from the panel that the agent reports: [{id, sha256, hosts}].
            $table->json('agent_certificates')->nullable();
            // The tries to send the certificates again after a report with other certificates.
            $table->json('certificates_resync')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn(['agent_certificates', 'certificates_resync']));
        Schema::dropIfExists('certificates');
    }
};
