<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The desired state that the panel keeps (spec section 6.3).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('plans', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('name');
            $table->json('limits');
            $table->unsignedInteger('websites');
            $table->unsignedInteger('mailboxes');
            $table->unsignedInteger('databases');
            $table->json('php_extensions');
            $table->timestamps();
        });

        Schema::create('customers', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('name');
            $table->string('email');
            $table->string('role');
            $table->foreignUlid('plan_id')->constrained();
            $table->ulid('reseller_id')->nullable()->index();
            $table->json('branding')->nullable();
            $table->boolean('suspended')->default(false);
            $table->timestamps();
        });

        Schema::create('servers', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('name')->unique();
            $table->string('location')->default('');
            $table->json('roles');
            $table->string('state')->default('joining');
            $table->string('agent_url')->nullable();
            $table->string('agent_version')->nullable();
            $table->string('certificate_fingerprint')->nullable();
            $table->string('agent_token_hash')->nullable();
            $table->string('os')->nullable();
            $table->string('kernel')->nullable();
            $table->string('arch')->nullable();
            $table->string('podman_version')->nullable();
            $table->string('gvisor_version')->nullable();
            $table->string('gvisor_platform')->default('systrap');
            $table->boolean('bare_metal')->default(false);
            $table->string('update_policy')->default('security');
            $table->string('maintenance_window')->default('02:00-05:00');
            $table->json('pending_updates')->nullable();
            $table->boolean('reboot_required')->default(false);
            $table->json('services')->nullable();
            $table->json('usage')->nullable();
            $table->timestamp('last_update_at')->nullable();
            $table->timestamp('last_seen_at')->nullable();
            $table->timestamps();
        });

        Schema::create('join_tokens', function (Blueprint $table) {
            $table->id();
            $table->string('token_hash')->unique();
            $table->timestamp('expires_at');
            $table->timestamp('used_at')->nullable();
            $table->timestamps();
        });

        Schema::create('images', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('name');
            $table->string('family');
            $table->string('reference');
            $table->string('tag');
            $table->string('digest')->nullable();
            $table->string('available_tag')->nullable();
            $table->string('available_digest')->nullable();
            $table->string('php_version')->nullable();
            $table->date('support_ends_at')->nullable();
            $table->string('policy')->default('manual');
            $table->boolean('custom')->default(false);
            $table->boolean('signature_verified')->default(false);
            $table->timestamps();
        });

        Schema::create('websites', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->string('name')->unique();
            $table->string('application')->default('');
            $table->foreignUlid('customer_id')->constrained();
            $table->foreignUlid('server_id')->constrained();
            $table->foreignUlid('plan_id')->constrained();
            $table->foreignUlid('image_id')->constrained();
            $table->string('image_tag')->nullable();
            $table->foreignUlid('previous_image_id')->nullable()->constrained('images');
            $table->string('previous_image_tag')->nullable();
            $table->string('unix_user')->unique();
            $table->string('status')->default('starting');
            $table->boolean('worker_mode')->default(false);
            $table->json('extensions');
            $table->json('php_ini');
            $table->json('limits');
            $table->json('usage')->nullable();
            $table->ulid('staging_of')->nullable();
            $table->boolean('managed_by_terraform')->default(false);
            $table->timestamps();
        });

        Schema::create('domains', function (Blueprint $table) {
            $table->id();
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            $table->string('name')->unique();
            $table->string('kind');
            $table->boolean('dns_points_here')->default(false);
            $table->string('certificate_state')->default('pending');
            $table->timestamp('certificate_expires_at')->nullable();
            $table->timestamps();
        });

        Schema::create('databases', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            $table->string('engine');
            $table->string('name');
            $table->unsignedBigInteger('size_bytes')->default(0);
            $table->timestamps();
            $table->unique(['engine', 'name']);
        });

        Schema::create('database_users', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            $table->string('engine');
            $table->string('name');
            $table->json('databases');
            // FR-502: by default only the website container can connect.
            $table->string('access')->default('website');
            $table->timestamps();
            $table->unique(['engine', 'name']);
        });

        Schema::create('releases', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            $table->string('ref');
            $table->string('message')->default('');
            $table->string('state');
            $table->boolean('live')->default(false);
            $table->boolean('migration')->default(false);
            $table->unsignedInteger('duration_seconds')->default(0);
            $table->timestamps();
        });

        Schema::create('snapshots', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            $table->string('kind');
            $table->string('restic_id')->nullable();
            $table->unsignedBigInteger('size_bytes')->default(0);
            $table->json('databases');
            $table->json('mailboxes');
            $table->timestamps();
        });

        Schema::create('rollouts', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('image_id')->constrained();
            $table->string('from_tag');
            $table->string('to_tag');
            $table->string('severity')->default('security');
            $table->string('state')->default('running');
            $table->unsignedTinyInteger('stage')->default(0);
            $table->json('website_ids');
            $table->json('updated_ids');
            $table->json('rolled_back');
            $table->timestamps();
        });

        Schema::create('agent_jobs', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignUlid('server_id')->constrained();
            $table->ulid('website_id')->nullable()->index();
            $table->ulid('rollout_id')->nullable()->index();
            $table->string('action');
            $table->string('title');
            $table->string('ref')->nullable();
            $table->text('payload');
            $table->json('steps');
            $table->unsignedInteger('step')->default(0);
            $table->string('state')->default('queued');
            $table->text('error')->nullable();
            $table->timestamp('started_at')->nullable();
            $table->timestamp('finished_at')->nullable();
            $table->timestamps();
        });

        Schema::create('audit_entries', function (Blueprint $table) {
            $table->id();
            $table->unsignedBigInteger('user_id')->nullable();
            $table->string('actor');
            $table->string('ip', 45)->nullable();
            $table->string('action');
            $table->string('target')->default('');
            $table->ulid('customer_id')->nullable()->index();
            $table->timestamp('created_at')->useCurrent();
        });

        Schema::create('settings', function (Blueprint $table) {
            $table->string('key')->primary();
            $table->json('value');
            $table->timestamps();
        });
    }

    public function down(): void
    {
        foreach (['settings', 'audit_entries', 'agent_jobs', 'rollouts', 'snapshots', 'releases', 'database_users', 'databases', 'domains', 'websites', 'images', 'join_tokens', 'servers', 'customers', 'plans'] as $table) {
            Schema::dropIfExists($table);
        }
    }
};
