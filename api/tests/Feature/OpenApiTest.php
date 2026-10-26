<?php

namespace Tests\Feature;

use Illuminate\Support\Facades\Route;
use Tests\TestCase;

/** FR-103: the OpenAPI document describes each route of the API, and no route that does not exist. */
class OpenApiTest extends TestCase
{
    public function test_the_document_and_the_routes_agree(): void
    {
        $documented = self::documentedOperations(base_path('../openapi/openapi.yaml'));

        $routes = [];
        foreach (Route::getRoutes() as $route) {
            if (! str_starts_with($route->uri(), 'api/')) {
                continue;
            }
            foreach (array_diff($route->methods(), ['HEAD']) as $method) {
                $routes[] = strtolower($method).' /'.substr($route->uri(), 4);
            }
        }
        sort($routes);

        $this->assertSame([], array_values(array_diff($routes, $documented)), 'These routes are not in openapi/openapi.yaml.');
        $this->assertSame([], array_values(array_diff($documented, $routes)), 'openapi/openapi.yaml has routes that do not exist.');
    }

    /**
     * Reads the operations from the "paths" section. Paths have two spaces of indent, and
     * methods have four. The repository has no YAML parser for PHP, and this is sufficient.
     *
     * @return list<string>
     */
    private static function documentedOperations(string $file): array
    {
        $operations = [];
        $path = null;
        $inPaths = false;
        foreach (file($file, FILE_IGNORE_NEW_LINES) as $line) {
            if (preg_match('/^(\w+):/', $line, $m)) {
                $inPaths = $m[1] === 'paths';

                continue;
            }
            if (! $inPaths) {
                continue;
            }
            if (preg_match('#^  (/\S+):\s*$#', $line, $m)) {
                $path = $m[1];
            } elseif ($path && preg_match('/^    (get|post|put|patch|delete):/', $line, $m)) {
                $operations[] = "{$m[1]} {$path}";
            }
        }
        sort($operations);

        return $operations;
    }
}
