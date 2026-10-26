<?php

namespace App\Http\Resources;

use App\Models\Domain;
use App\Models\Website;
use Illuminate\Http\Request;
use Illuminate\Http\Resources\Json\JsonResource;

/** @mixin Website */
class WebsiteResource extends JsonResource
{
    public function toArray(Request $request): array
    {
        $image = $this->image;

        return [
            'id' => $this->id,
            'name' => $this->name,
            'application' => $this->application,
            'customerId' => $this->customer_id,
            'serverId' => $this->server_id,
            'planId' => $this->plan_id,
            'unixUser' => $this->unix_user,
            'status' => $this->status->value,
            'runtime' => [
                'imageId' => $this->image_id,
                'family' => $image->family,
                'image' => $image->reference.':'.$this->currentTag(),
                'digest' => $image->digest,
                'signatureVerified' => $image->signature_verified,
                'phpVersion' => $image->php_version,
                'workerMode' => $this->worker_mode,
                'extensions' => $this->extensions,
                'phpIni' => (object) $this->php_ini,
                'previousImageId' => $this->previous_image_id,
                'supportEndsAt' => $image->support_ends_at?->toDateString(),
            ],
            'updateReady' => $this->updateReady(),
            'limits' => $this->limits,
            'usage' => $this->usage,
            'domains' => $this->whenLoaded('domains', fn () => $this->domains->map(fn (Domain $d) => self::domain($d))),
            'stagingOf' => $this->staging_of,
            'managedByTerraform' => $this->managed_by_terraform,
            'createdAt' => $this->created_at?->toIso8601String(),
            'updatedAt' => $this->updated_at?->toIso8601String(),
        ];
    }

    /** CF-03: a domain that is not verified shows the TXT record that the customer must add. */
    public static function domain(Domain $d): array
    {
        return [
            'name' => $d->name,
            'kind' => $d->kind->value,
            'verified' => $d->verified(),
            'verification' => $d->verified() ? null : ['type' => 'TXT', 'name' => $d->verificationRecord(), 'value' => $d->verification_token],
            'dnsPointsHere' => $d->dns_points_here,
            'certificate' => ['state' => $d->certificate_state, 'expiresAt' => $d->certificate_expires_at?->toIso8601String()],
        ];
    }
}
