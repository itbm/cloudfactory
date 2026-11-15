<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Services\Audit;
use App\Services\Branding;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/**
 * White label. The sign-in page reads the brand of the domain that the browser opened. The
 * administrator sets the default brand. A reseller sets its own brand on its customer record
 * (CustomerController).
 */
class BrandController extends Controller
{
    /**
     * The brand of the sign-in page: the brand of the reseller that has this panel domain, or
     * the default brand. A person who has not signed in can read it.
     */
    public function show(Request $request): JsonResponse
    {
        return response()->json(Branding::forHost($request->getHost()))
            ->header('Cache-Control', 'no-cache');
    }

    /** The default brand. Only an administrator can read or change it. */
    public function showDefault(): JsonResponse
    {
        return response()->json(Branding::defaults());
    }

    public function updateDefault(Request $request): JsonResponse
    {
        $data = $request->validate(Branding::rules());
        if (isset($data['logo'])) {
            $data['logo'] = Branding::logo($data['logo'], 'logo');
        }
        Branding::saveDefaults($data);
        Audit::record($request, 'Changed the default brand', Branding::defaults()['name']);

        return response()->json(Branding::defaults());
    }
}
