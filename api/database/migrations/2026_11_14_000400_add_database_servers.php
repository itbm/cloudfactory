<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;

/**
 * A customer has database servers, as it has websites. A database server is one database
 * container with one engine (MariaDB, PostgreSQL or Valkey) on a server. It has its own limits
 * for CPU, memory and disk space, and each database is in one database server.
 *
 * A plan gives the number of database servers of a customer ("database_servers"), the limits
 * of each database server ("database_limits") and the number of databases of each database
 * server ("databases"). Before, "databases" was the number of databases of each website.
 *
 * "database_server_id" of a database and of a database user is null for a website that uses
 * the shared database containers of an earlier release.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('plans', function (Blueprint $table) {
            $table->unsignedInteger('database_servers')->default(3);
            $table->json('database_limits')->nullable();
        });
        // A plan from before this change gets database servers with the vCPUs of a website, the
        // half of its memory and the half of its disk space. A database server needs 512 MB of
        // memory and 1 GB of disk space.
        foreach (DB::table('plans')->get(['id', 'limits']) as $plan) {
            $limits = json_decode((string) $plan->limits, true) ?: [];
            DB::table('plans')->where('id', $plan->id)->update(['database_limits' => json_encode([
                'vcpus' => max(0.25, (float) ($limits['vcpus'] ?? 1)),
                'memoryBytes' => max(512 * 1024 ** 2, intdiv((int) ($limits['memoryBytes'] ?? 0), 2)),
                'diskBytes' => max(1024 ** 3, intdiv((int) ($limits['diskBytes'] ?? 0), 2)),
            ])]);
        }

        Schema::create('database_servers', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('customer_id')->constrained();
            // The server that has the container. The websites of the customer on this server
            // can connect to the database server.
            $table->foreignUlid('server_id')->constrained();
            $table->foreignUlid('plan_id')->constrained();
            $table->string('engine');
            $table->string('name');
            // The port on the address of the customer. The first database server of an engine
            // has the usual port of the engine.
            $table->unsignedInteger('port');
            // "vcpus", "memoryBytes" and "diskBytes".
            $table->json('limits');
            $table->string('status')->default('starting');
            // The last report of the agent: "memoryBytes", "diskBytes" and "diskSizeBytes".
            $table->json('usage')->nullable();
            $table->timestamps();
            $table->unique(['customer_id', 'server_id', 'port']);
        });
        Schema::table('databases', function (Blueprint $table) {
            $table->foreignUlid('database_server_id')->nullable()->constrained();
        });
        Schema::table('database_users', function (Blueprint $table) {
            $table->foreignUlid('database_server_id')->nullable()->constrained();
        });
        // The job of a database server has no website. The owner of the database server can
        // see the job.
        Schema::table('agent_jobs', function (Blueprint $table) {
            $table->ulid('database_server_id')->nullable()->index();
        });
    }

    public function down(): void
    {
        // SQLite needs the index removed before the column.
        Schema::table('agent_jobs', fn (Blueprint $table) => $table->dropIndex(['database_server_id']));
        Schema::table('agent_jobs', fn (Blueprint $table) => $table->dropColumn('database_server_id'));
        Schema::table('database_users', fn (Blueprint $table) => $table->dropConstrainedForeignId('database_server_id'));
        Schema::table('databases', fn (Blueprint $table) => $table->dropConstrainedForeignId('database_server_id'));
        Schema::dropIfExists('database_servers');
        Schema::table('plans', fn (Blueprint $table) => $table->dropColumn(['database_servers', 'database_limits']));
    }
};
