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

    /**
     * Named events. Each one is a change of the audit log with one action. A plugin that wants
     * only some changes gives these names, and does not need to read each change of the log.
     * The data is the same as the data of "audit.recorded".
     */
    public const NAMED = [
        'Created website' => 'website.created',
        // A preview of a pull request is a website of the plan too.
        'Made preview' => 'website.created',
        'Changed website' => 'website.changed',
        'Removed website' => 'website.removed',
        'Removed preview' => 'website.removed',
        'Added domain' => 'domain.added',
        'Verified domain' => 'domain.verified',
        'Removed domain' => 'domain.removed',
        'Created database' => 'database.created',
        'Removed database' => 'database.removed',
        'Backed up website' => 'backup.started',
        'Restored from backup' => 'restore.started',
        'Started deploy' => 'deploy.started',
        'Started deploy from webhook' => 'deploy.started',
        'Started deploy from branch check' => 'deploy.started',
        'Created customer' => 'customer.created',
        'Changed customer' => 'customer.changed',
        'Created user' => 'user.created',
    ];

    /** The events that a manifest can give. */
    public const NAMES = [
        self::AUDIT_RECORDED, self::JOB_FINISHED,
        'website.created', 'website.changed', 'website.removed',
        'domain.added', 'domain.verified', 'domain.removed',
        'database.created', 'database.removed',
        'backup.started', 'restore.started', 'deploy.started',
        'customer.created', 'customer.changed', 'user.created',
    ];

    /**
     * Records the event for each plugin that is on and wants it, and puts the deliveries in the
     * queue. The deliveries start after the transaction of the change.
     *
     * @param  array<string, mixed>  $data
     */
    public static function emit(string $event, array $data): void
    {
        $plugins = Plugin::where('enabled', true)->get()
            ->filter(fn (Plugin $p) => in_array($event, $p->events(), true) && Manifest::permits($p->scopes(), $event));
        foreach ($plugins as $plugin) {
            self::deliver($plugin, $event, $data);
        }
    }

    /** @param  array<string, mixed>  $data */
    public static function deliver(Plugin $plugin, string $event, array $data): PluginDelivery
    {
        $id = strtolower((string) Str::ulid());
        // Adversarial review, finding A7-06: the ID in the body is the ID of the delivery.
        $delivery = (new PluginDelivery)->forceFill(['id' => $id])->fill([
            'plugin_id' => $plugin->id,
            'event' => $event,
            'state' => PluginDelivery::PENDING,
            'attempts' => 0,
            'payload' => ['id' => $id, 'event' => $event, 'createdAt' => now()->toIso8601String(), 'data' => $data],
        ]);
        $delivery->save();
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
