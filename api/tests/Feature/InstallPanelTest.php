<?php

namespace Tests\Feature;

use App\Models\Image;
use App\Models\Plan;
use App\Models\Rollout;
use App\Models\User;
use Illuminate\Support\Facades\Artisan;
use Tests\TestCase;

class InstallPanelTest extends TestCase
{
    public function test_install_adds_the_administrator_images_plans_and_a_join_token(): void
    {
        Artisan::call('cloudfactory:install', ['email' => 'admin@example.com']);
        $out = json_decode(trim(Artisan::output()), true);

        $this->assertSame(20, strlen($out['password']));
        $this->assertStringStartsWith('cfj_', $out['joinToken']);
        $this->assertSame(4, Image::count());
        $this->assertSame(3, Plan::count());
        $this->assertTrue(User::sole()->isAdministrator());

        // A second run does not add a second administrator or change the password.
        Artisan::call('cloudfactory:install', ['email' => 'other@example.com']);
        $this->assertNull(json_decode(trim(Artisan::output()), true)['password']);
        $this->assertSame(1, User::count());
        $this->assertSame(4, Image::count());
    }

    /** An update of the panel runs the command again with the tag of the new release. */
    public function test_an_update_moves_the_images_to_the_new_release(): void
    {
        Artisan::call('cloudfactory:install', ['email' => 'admin@example.com', '--image-tag' => '0.1.7']);
        $used = Image::where('family', 'frankenphp')->where('php_version', '8.4')->sole();
        $website = $this->website($this->customer(), $this->server(), $used);
        $manual = Image::where('family', 'apache-php-fpm')->where('php_version', '8.4')->sole();
        $manual->update(['policy' => 'manual']);

        Artisan::call('cloudfactory:install', ['email' => 'admin@example.com', '--image-tag' => '0.1.9']);

        // No website uses these images: they change at once, and the old version does not stay.
        foreach (Image::whereNotIn('id', [$used->id, $manual->id])->get() as $image) {
            $this->assertSame('0.1.9', $image->tag, $image->name);
            $this->assertNull($image->available_tag, $image->name);
        }
        // A website uses this image: the panel offers the rollout and does not change the website.
        $this->assertSame('0.1.7', $used->fresh()->tag);
        $this->assertSame('0.1.9', $used->fresh()->available_tag);
        $this->assertSame('0.1.7', $website->fresh()->currentTag());
        $this->assertSame(0, Rollout::count());
        // The manual policy waits for the administrator.
        $this->assertSame('0.1.7', $manual->fresh()->tag);
        $this->assertSame('0.1.9', $manual->fresh()->available_tag);

        // An installation of an older release does not put the images back.
        Artisan::call('cloudfactory:install', ['email' => 'admin@example.com', '--image-tag' => '0.1.8']);
        $this->assertSame(2, Image::where('tag', '0.1.9')->count());
        $this->assertSame('0.1.9', $used->fresh()->available_tag);
    }
}
