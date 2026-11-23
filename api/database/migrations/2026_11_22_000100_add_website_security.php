<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The protection of a website in the edge proxy (App\Services\WebsiteSecurity): the firewall,
 * the IP address rules, the password-protected paths and the sign-in limit. Null is no
 * protection. The value has the bcrypt hashes of the passwords of the protected paths, so the
 * API never gives it as it is.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('websites', function (Blueprint $table) {
            $table->json('security')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('websites', function (Blueprint $table) {
            $table->dropColumn('security');
        });
    }
};
