<?php

namespace App\Services\Plugins;

use App\Jobs\DeliverPluginEvent;
use App\Models\Plugin;
use App\Models\PluginDelivery;
use Illuminate\Support\Str;

/**
 * The events that the panel sends to plugins. A plugin gives the events that it wants in its
 * manifest. The panel sends each event with a signed webhook (DeliverPluginEvent).
 */
class Events
{
    /** A change in the audit log: each change that a user, a token or the panel makes. */
    public const AUDIT_RECORDED = 'audit.recorded';

    /** A job of an agent is complete: it succeeded or it failed. */
    public const JOB_FINISHED = 'job.finished';

    /** A test event. The administrator sends it from the page of the plugin. */
    public const TEST = 'plugin.test';

    /** The events that a manifest can give. */
    public const NAMES = [self::AUDIT_RECORDED, self::JOB_FINISHED];

    /**
     * Records the event for each plugin that is on and wants it, and puts the deliveries in the
     * queue. The deliveries start after the transaction of the change.
     *
     * @param  array<string, mixed>  $data
     */
    public static function emit(string $event, array $data): void
    {
        $plugins = Plugin::where('enabled', true)->get()->filter(fn (Plugin $p) => in_array($event, $p->events(), true));
        foreach ($plugins as $plugin) {
            self::deliver($plugin, $event, $data);
        }
    }

    /** @param  array<string, mixed>  $data */
    public static function deliver(Plugin $plugin, string $event, array $data): PluginDelivery
    {
        $id = (string) Str::ulid();
        $delivery = PluginDelivery::create([
            'id' => $id,
            'plugin_id' => $plugin->id,
            'event' => $event,
            'state' => PluginDelivery::PENDING,
            'attempts' => 0,
            'payload' => ['id' => $id, 'event' => $event, 'createdAt' => now()->toIso8601String(), 'data' => $data],
        ]);
        DeliverPluginEvent::dispatch($delivery->id)->afterCommit();

        return $delivery;
    }

    /**
     * The signature header of a delivery: "t=<Unix time>,v1=<HMAC-SHA256 of "<t>.<body>">".
     * The plugin computes the same value with its secret and refuses a delivery that is older
     * than 5 minutes.
     */
    public static function signature(string $secret, int $time, string $body): string
    {
        return "t={$time},v1=".hash_hmac('sha256', "{$time}.{$body}", $secret);
    }
}
