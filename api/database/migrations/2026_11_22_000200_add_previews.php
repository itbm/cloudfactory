<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The preview websites of pull requests.
 *
 * A website that deploys from Git can make a preview for each pull request to its branch. A
 * preview is a website of its own: its own container, its own Unix user, the plan of the
 * customer, and the name "pr-<number>.<preview domain>". It deploys the head branch of the pull
 * request. The panel removes it when the pull request closes.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('git_sources', function (Blueprint $table) {
            // True: a pull request to the branch makes a preview website.
            $table->boolean('previews')->default(false);
            // The domain of the previews. A preview gets the name "pr-<number>.<this domain>".
            $table->string('preview_domain', 200)->nullable();
            // The largest number of previews of the website at the same time.
            $table->unsignedSmallInteger('preview_limit')->default(3);
            // True: the previews get the environment variables and the build secrets of the website.
            $table->boolean('preview_environment')->default(false);
        });

        Schema::create('previews', function (Blueprint $table) {
            $table->ulid('id')->primary();
            // The website of the Git source.
            $table->foreignUlid('website_id')->constrained()->cascadeOnDelete();
            // The preview website. Null when the panel could not make it: "error" has the cause.
            $table->foreignUlid('preview_website_id')->nullable()->unique()->constrained('websites')->cascadeOnDelete();
            // The number of the pull request on the Git host.
            $table->unsignedInteger('number');
            // The head branch of the pull request.
            $table->string('branch', 100);
            $table->string('title', 200)->default('');
            // The page of the pull request on the Git host.
            $table->string('url', 500)->nullable();
            // A push came while a deploy of the preview was in progress: deploy again after it.
            $table->boolean('deploy_again')->default(false);
            $table->text('error')->nullable();
            $table->timestamps();
            $table->unique(['website_id', 'number']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('previews');
        Schema::table('git_sources', fn (Blueprint $table) => $table->dropColumn(['previews', 'preview_domain', 'preview_limit', 'preview_environment']));
    }
};
