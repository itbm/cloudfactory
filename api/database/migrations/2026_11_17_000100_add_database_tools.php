<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The database tool (Adminer). Each customer has one Adminer container on the panel server,
 * with its own Unix user, in its own gVisor sandbox. It connects only to the database servers
 * of its customer. A user opens a database in Adminer from the panel, with a temporary
 * database user that the panel makes for that one database.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('database_tools', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->unique()->constrained()->cascadeOnDelete();
            $table->foreignUlid('server_id')->constrained('servers');
            $table->string('unix_user')->unique();
            // The tag of the Adminer image: the version of the agent of the panel server.
            $table->string('image_tag');
            $table->string('status');
            $table->timestamps();
        });

        Schema::create('database_tool_logins', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('database_tool_id')->constrained()->cascadeOnDelete();
            // The database and its website. The row stays when they go, so that the panel
            // still removes the temporary database user from the database server.
            $table->foreignUlid('database_id')->nullable()->constrained()->nullOnDelete();
            $table->foreignUlid('website_id')->nullable()->constrained()->nullOnDelete();
            $table->string('unix_user');
            $table->foreignUlid('database_server_id')->constrained('database_servers')->cascadeOnDelete();
            $table->foreignId('user_id')->nullable()->constrained()->nullOnDelete();
            $table->string('engine');
            $table->string('database_name');
            // "readonly" or "readwrite", as for a database user.
            $table->string('privileges');
            // The temporary database user. The panel does not keep its password.
            $table->string('name')->unique();
            $table->string('status');
            $table->timestamp('expires_at')->index();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('database_tool_logins');
        Schema::dropIfExists('database_tools');
    }
};
