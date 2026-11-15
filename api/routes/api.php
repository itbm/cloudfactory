<?php

use App\Http\Controllers\Agent\AgentController;
use App\Http\Controllers\Agent\JoinController;
use App\Http\Controllers\Api\AccountController;
use App\Http\Controllers\Api\AuditController;
use App\Http\Controllers\Api\AuthController;
use App\Http\Controllers\Api\BackupController;
use App\Http\Controllers\Api\CronJobController;
use App\Http\Controllers\Api\CustomerController;
use App\Http\Controllers\Api\DatabaseController;
use App\Http\Controllers\Api\DatabaseServerController;
use App\Http\Controllers\Api\DatabaseUserController;
use App\Http\Controllers\Api\DeployController;
use App\Http\Controllers\Api\DnsZoneController;
use App\Http\Controllers\Api\DomainController;
use App\Http\Controllers\Api\FileController;
use App\Http\Controllers\Api\ImageController;
use App\Http\Controllers\Api\JobController;
use App\Http\Controllers\Api\LogController;
use App\Http\Controllers\Api\MeshController;
use App\Http\Controllers\Api\NetworkController;
use App\Http\Controllers\Api\PanelCertificateController;
use App\Http\Controllers\Api\PlanController;
use App\Http\Controllers\Api\RolloutController;
use App\Http\Controllers\Api\RuntimeController;
use App\Http\Controllers\Api\ServerController;
use App\Http\Controllers\Api\SetupController;
use App\Http\Controllers\Api\SftpController;
use App\Http\Controllers\Api\UsageController;
use App\Http\Controllers\Api\UserController;
use App\Http\Controllers\Api\WebsiteController;
use Illuminate\Support\Facades\Route;

/*
| The public API (spec section 6.3, FR-103). The web panel and the cf command line tool use only
| these routes. openapi/openapi.yaml describes each route, and a test checks that they agree.
*/

Route::prefix('v1')->group(function () {
    Route::post('auth/login', [AuthController::class, 'login'])->middleware('throttle:10,1');
    // The webhook of a website: a Git host calls it after a push. The token in the address is
    // the only credential.
    Route::post('hooks/deploy/{token}', [DeployController::class, 'hook'])->middleware('throttle:30,1');

    Route::middleware(['auth:sanctum', 'active'])->group(function () {
        Route::post('auth/logout', [AuthController::class, 'logout']);
        Route::get('me', [AuthController::class, 'me']);
        // R-01: the user changes the password and turns TOTP on or off. Only a sign-in session can.
        Route::middleware('throttle:10,1')->group(function () {
            Route::put('me/password', [AuthController::class, 'changePassword']);
            Route::post('me/totp/setup', [AuthController::class, 'setupTotp']);
            Route::post('me/totp/enable', [AuthController::class, 'enableTotp']);
            Route::post('me/totp/disable', [AuthController::class, 'disableTotp']);
        });
        Route::middleware('scope:tokens')->group(function () {
            Route::get('tokens', [AuthController::class, 'tokens']);
            Route::post('tokens', [AuthController::class, 'createToken'])->middleware('throttle:10,1');
            Route::delete('tokens/{id}', [AuthController::class, 'revokeToken'])->whereNumber('id');
        });

        Route::middleware('scope:websites')->group(function () {
            Route::get('websites', [WebsiteController::class, 'index']);
            Route::post('websites', [WebsiteController::class, 'store']);
            Route::get('websites/{id}', [WebsiteController::class, 'show']);
            Route::patch('websites/{id}', [WebsiteController::class, 'update']);
            Route::delete('websites/{id}', [WebsiteController::class, 'destroy']);
            Route::post('websites/{id}/power', [WebsiteController::class, 'power']);
            Route::post('websites/{id}/retry', [WebsiteController::class, 'retry']);
            Route::post('websites/{id}/staging', [WebsiteController::class, 'staging']);
            // Audit finding S5: limits for each customer (AppServiceProvider).
            Route::post('websites/{websiteId}/domains', [DomainController::class, 'store'])->middleware('throttle:domain-changes');
            Route::post('websites/{websiteId}/domains/{name}/verify', [DomainController::class, 'verify'])->middleware('throttle:domain-checks');
            Route::post('websites/{websiteId}/domains/{name}/primary', [DomainController::class, 'makePrimary']);
            Route::post('websites/{websiteId}/domains/{name}/check', [DomainController::class, 'check'])->middleware('throttle:domain-checks');
            Route::delete('websites/{websiteId}/domains/{name}', [DomainController::class, 'destroy']);
            Route::put('websites/{websiteId}/sftp', [SftpController::class, 'update']);
            Route::put('websites/{websiteId}/runtime', [RuntimeController::class, 'update']);
            Route::post('websites/{websiteId}/runtime/revert', [RuntimeController::class, 'revert']);

            // The file manager and the logs. The agent does each request as the Unix user of
            // the website, and gives the answer at once.
            Route::get('websites/{websiteId}/files', [FileController::class, 'index'])->middleware('throttle:site-tools');
            Route::delete('websites/{websiteId}/files', [FileController::class, 'destroy'])->middleware('throttle:site-tools');
            Route::get('websites/{websiteId}/files/content', [FileController::class, 'show'])->middleware('throttle:site-tools');
            Route::put('websites/{websiteId}/files/content', [FileController::class, 'store'])->middleware('throttle:site-tools');
            Route::post('websites/{websiteId}/files/directories', [FileController::class, 'makeDirectory'])->middleware('throttle:site-tools');
            Route::post('websites/{websiteId}/files/move', [FileController::class, 'move'])->middleware('throttle:site-tools');
            Route::get('websites/{websiteId}/logs', [LogController::class, 'index'])->middleware('throttle:site-tools');
            Route::get('websites/{websiteId}/usage', [UsageController::class, 'website']);
            Route::get('usage-alerts', [UsageController::class, 'alerts']);

            Route::get('websites/{websiteId}/cron-jobs', [CronJobController::class, 'index']);
            Route::post('websites/{websiteId}/cron-jobs', [CronJobController::class, 'store']);
            Route::patch('websites/{websiteId}/cron-jobs/{cronJobId}', [CronJobController::class, 'update']);
            Route::delete('websites/{websiteId}/cron-jobs/{cronJobId}', [CronJobController::class, 'destroy']);
            Route::post('websites/{websiteId}/cron-jobs/{cronJobId}/run', [CronJobController::class, 'run'])->middleware('throttle:30,1');

            // Deploys from a Git repository.
            Route::get('websites/{websiteId}/deploy', [DeployController::class, 'show']);
            Route::put('websites/{websiteId}/deploy', [DeployController::class, 'update']);
            Route::delete('websites/{websiteId}/deploy', [DeployController::class, 'destroy']);
            Route::put('websites/{websiteId}/deploy/secrets/{name}', [DeployController::class, 'setSecret']);
            Route::delete('websites/{websiteId}/deploy/secrets/{name}', [DeployController::class, 'removeSecret']);
            Route::post('websites/{websiteId}/deploy/releases', [DeployController::class, 'start'])->middleware('throttle:30,1');
            Route::post('websites/{websiteId}/deploy/releases/{releaseId}/activate', [DeployController::class, 'activate'])->middleware('throttle:30,1');
            Route::get('runtime-images', [ImageController::class, 'runtimes']);
            Route::get('jobs', [JobController::class, 'index']);
            Route::get('jobs/{id}', [JobController::class, 'show']);
        });

        Route::middleware('scope:databases')->group(function () {
            Route::get('websites/{websiteId}/databases', [DatabaseController::class, 'index']);
            Route::post('websites/{websiteId}/databases', [DatabaseController::class, 'store']);
            Route::delete('websites/{websiteId}/databases/{databaseId}', [DatabaseController::class, 'destroy']);
            Route::post('websites/{websiteId}/database-users', [DatabaseUserController::class, 'store']);
            Route::patch('websites/{websiteId}/database-users/{userId}', [DatabaseUserController::class, 'update']);
            Route::delete('websites/{websiteId}/database-users/{userId}', [DatabaseUserController::class, 'destroy']);
            // The database servers of a customer: containers with their own limits, as websites.
            // The private networks of the customers (VPC).
            Route::get('networks', [NetworkController::class, 'index']);
            Route::post('networks', [NetworkController::class, 'store']);
            Route::get('networks/{id}', [NetworkController::class, 'show']);
            Route::patch('networks/{id}', [NetworkController::class, 'update']);
            Route::delete('networks/{id}', [NetworkController::class, 'destroy']);
            Route::get('database-servers', [DatabaseServerController::class, 'index']);
            Route::post('database-servers', [DatabaseServerController::class, 'store']);
            Route::get('database-servers/{id}', [DatabaseServerController::class, 'show']);
            Route::patch('database-servers/{id}', [DatabaseServerController::class, 'update']);
            Route::delete('database-servers/{id}', [DatabaseServerController::class, 'destroy']);
            Route::post('database-servers/{id}/retry', [DatabaseServerController::class, 'retry']);
            Route::post('database-servers/{id}/move', [DatabaseServerController::class, 'move']);
            Route::get('database-servers/{id}/usage', [UsageController::class, 'databaseServer']);
        });

        // DNS hosting: the zones of a customer and their records.
        Route::middleware('scope:dns')->group(function () {
            Route::get('dns-zones', [DnsZoneController::class, 'index']);
            Route::post('dns-zones', [DnsZoneController::class, 'store'])->middleware('throttle:dns-changes');
            Route::get('dns-zones/{id}', [DnsZoneController::class, 'show']);
            Route::delete('dns-zones/{id}', [DnsZoneController::class, 'destroy']);
            Route::post('dns-zones/{id}/records', [DnsZoneController::class, 'storeRecord'])->middleware('throttle:dns-changes');
            Route::patch('dns-zones/{id}/records/{recordId}', [DnsZoneController::class, 'updateRecord'])->middleware('throttle:dns-changes');
            Route::delete('dns-zones/{id}/records/{recordId}', [DnsZoneController::class, 'destroyRecord'])->middleware('throttle:dns-changes');
            Route::put('dns-zones/{id}/dnssec', [DnsZoneController::class, 'dnssec'])->middleware('throttle:dns-changes');
            Route::post('dns-zones/{id}/check', [DnsZoneController::class, 'check'])->middleware('throttle:domain-checks');
            Route::post('dns-zones/{id}/sync', [DnsZoneController::class, 'sync'])->middleware('throttle:dns-changes');
        });

        Route::middleware('scope:backups')->group(function () {
            Route::get('websites/{websiteId}/snapshots', [BackupController::class, 'index']);
            Route::post('websites/{websiteId}/snapshots', [BackupController::class, 'store']);
            Route::post('websites/{websiteId}/snapshots/{snapshotId}/restore', [BackupController::class, 'restore']);
            Route::get('backup-policy', [BackupController::class, 'showPolicy']);
            Route::put('backup-policy', [BackupController::class, 'updatePolicy'])->middleware('admin');
            Route::get('backup-policy/repository-password', [BackupController::class, 'showRepositoryPassword'])->middleware('admin');
            // Second audit, finding F08: the key of one server for the object storage.
            Route::put('backup-policy/servers/{serverId}/credentials', [BackupController::class, 'updateServerCredentials'])->middleware('admin');
            Route::delete('backup-policy/servers/{serverId}/credentials', [BackupController::class, 'destroyServerCredentials'])->middleware('admin');
        });

        Route::middleware(['admin', 'scope:servers'])->group(function () {
            Route::get('servers', [ServerController::class, 'index']);
            Route::post('servers/join-tokens', [ServerController::class, 'joinToken']);
            Route::get('servers/{id}', [ServerController::class, 'show']);
            Route::patch('servers/{id}', [ServerController::class, 'update']);
            Route::post('servers/{id}/install-updates', [ServerController::class, 'installUpdates']);
            Route::post('servers/{id}/reboot', [ServerController::class, 'reboot']);
            Route::post('servers/{id}/update-agent', [ServerController::class, 'updateAgent']);
            Route::post('servers/{id}/check-connection', [ServerController::class, 'checkConnection']);
            Route::delete('servers/{id}', [ServerController::class, 'destroy']);
            Route::get('servers/{id}/usage', [UsageController::class, 'server']);
            Route::put('usage-alerts/settings', [UsageController::class, 'updateSettings']);
            // The mesh: the encrypted tunnels between the servers.
            Route::get('mesh', [MeshController::class, 'show']);
            Route::patch('mesh', [MeshController::class, 'update']);
            Route::patch('mesh/servers/{id}', [MeshController::class, 'updateServer']);
            Route::post('mesh/servers/{id}/rotate-key', [MeshController::class, 'rotateKey']);
            Route::post('mesh/tunnels', [MeshController::class, 'storeTunnel']);
            Route::post('mesh/tunnels/{id}/repair', [MeshController::class, 'repairTunnel']);
            Route::delete('mesh/tunnels/{id}', [MeshController::class, 'destroyTunnel']);
            // The TLS certificate of the panel domain: the CA of the panel or Let's Encrypt.
            Route::get('panel-certificate', [PanelCertificateController::class, 'show']);
            Route::put('panel-certificate', [PanelCertificateController::class, 'update'])->middleware('throttle:10,1');
            // The setup checklist of the panel, with the effect of each open item.
            Route::get('setup', [SetupController::class, 'show']);
        });

        Route::middleware(['admin', 'scope:images'])->group(function () {
            Route::get('images', [ImageController::class, 'index']);
            Route::post('images', [ImageController::class, 'store']);
            Route::patch('images/{id}', [ImageController::class, 'update']);
            Route::post('images/{id}/check', [ImageController::class, 'check']);
            Route::get('rollouts', [RolloutController::class, 'index']);
            Route::post('rollouts', [RolloutController::class, 'store']);
            Route::get('rollouts/{id}', [RolloutController::class, 'show']);
            Route::post('rollouts/{id}/pause', [RolloutController::class, 'pause']);
            Route::post('rollouts/{id}/resume', [RolloutController::class, 'resume']);
            Route::post('rollouts/{id}/roll-back', [RolloutController::class, 'rollBack']);
        });

        Route::middleware('scope:customers')->group(function () {
            // The customer of the signed-in user, with its plan, its usage and its users.
            Route::get('account', [AccountController::class, 'show']);
            Route::get('customers', [CustomerController::class, 'index']);
            Route::post('customers', [CustomerController::class, 'store']);
            Route::patch('customers/{id}', [CustomerController::class, 'update']);
            Route::get('plans', [PlanController::class, 'index']);
            Route::post('plans', [PlanController::class, 'store'])->middleware('admin');
            Route::patch('plans/{id}', [PlanController::class, 'update'])->middleware('admin');
        });

        // Audit finding B5: the users who can sign in.
        Route::middleware('scope:users')->group(function () {
            Route::get('users', [UserController::class, 'index']);
            Route::post('users', [UserController::class, 'store'])->middleware('throttle:30,1');
            Route::post('users/{id}/reset-password', [UserController::class, 'resetPassword'])->whereNumber('id')->middleware('throttle:30,1');
            Route::delete('users/{id}', [UserController::class, 'destroy'])->whereNumber('id');
        });

        Route::get('audit', [AuditController::class, 'index'])->middleware('scope:audit');
    });
});

/*
| Routes for agents (spec section 6.4). The agent uses its own token, not a user token.
*/
Route::prefix('agent/v1')->group(function () {
    Route::post('join', JoinController::class)->middleware('throttle:20,1');

    Route::middleware('agent')->group(function () {
        Route::post('jobs/{id}/events', [AgentController::class, 'jobEvent']);
        Route::post('report', [AgentController::class, 'report']);
    });
});
