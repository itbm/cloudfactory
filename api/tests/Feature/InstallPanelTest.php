<?php

namespace Tests\Feature;

use App\Models\Image;
use App\Models\Plan;
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
        $this->assertSame(8, Image::count());
        $this->assertSame(3, Plan::count());
        $this->assertTrue(User::sole()->isAdministrator());

        // A second run does not add a second administrator or change the password.
        Artisan::call('cloudfactory:install', ['email' => 'other@example.com']);
        $this->assertNull(json_decode(trim(Artisan::output()), true)['password']);
        $this->assertSame(1, User::count());
        $this->assertSame(8, Image::count());
    }
}
