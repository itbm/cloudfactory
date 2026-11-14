<?php

namespace App\Enums;

/**
 * Spec section 6.4: the agent accepts only typed actions. It never accepts shell commands.
 * Each value is a method of the agent service in agent/proto/cloudfactory/agent/v1/agent.proto.
 */
enum AgentAction: string
{
    case CreateWebsite = 'CreateWebsite';
    case DeleteWebsite = 'DeleteWebsite';
    case SetWebsitePower = 'SetWebsitePower';
    case SetLimits = 'SetLimits';
    case SetRuntime = 'SetRuntime';
    case AddDomain = 'AddDomain';
    case RemoveDomain = 'RemoveDomain';
    case CreateDatabase = 'CreateDatabase';
    case DeleteDatabase = 'DeleteDatabase';
    case SetDatabaseUser = 'SetDatabaseUser';
    case DeleteDatabaseUser = 'DeleteDatabaseUser';
    case Backup = 'Backup';
    case Restore = 'Restore';
    case InstallHostUpdates = 'InstallHostUpdates';
    case Reboot = 'Reboot';
    case UpdateAgent = 'UpdateAgent';
    case PruneBackups = 'PruneBackups';
    case SetSftp = 'SetSftp';
    case ConfigureHost = 'ConfigureHost';
    case SetPanelCertificate = 'SetPanelCertificate';
    case MoveDatabases = 'MoveDatabases';
    case SetDatabaseServer = 'SetDatabaseServer';
    case DeleteDatabaseServer = 'DeleteDatabaseServer';

    /** The steps that the agent reports for this action. */
    public function steps(): array
    {
        return match ($this) {
            self::CreateWebsite => ['Create Unix user', 'Copy databases', 'Pull and check image signature', 'Start container in gVisor', 'Configure edge proxy', 'Get TLS certificate'],
            self::DeleteWebsite => ['Stop container', 'Remove edge proxy route', 'Remove Unix user'],
            self::SetWebsitePower => ['Change container state', 'Health check'],
            self::SetLimits => ['Update cgroup limits'],
            self::SetRuntime => ['Check image signature', 'Start new container on a copy', 'Health check', 'Switch traffic', 'Stop old container'],
            self::AddDomain => ['Add edge proxy route', 'Check DNS', 'Get TLS certificate'],
            self::RemoveDomain => ['Remove edge proxy route'],
            self::CreateDatabase => ['Create database', 'Create user', 'Allow the website container only'],
            self::MoveDatabases => ['Start database servers', 'Stop website', 'Copy databases', 'Copy users', 'Start website', 'Remove old copies'],
            self::DeleteDatabase => ['Remove access of users', 'Remove database'],
            self::SetDatabaseServer => ['Start database server'],
            self::DeleteDatabaseServer => ['Stop database server', 'Remove data'],
            self::SetDatabaseUser => ['Set user and password', 'Set access to databases'],
            self::DeleteDatabaseUser => ['Remove user'],
            self::Backup => ['Snapshot files', 'Dump databases', 'Upload to object storage', 'Apply retention'],
            self::Restore => ['Download snapshot', 'Restore', 'Health check'],
            self::InstallHostUpdates => ['Download packages', 'Install packages', 'Restart changed services only'],
            self::Reboot => ['Drain the edge proxy', 'Stop containers', 'Reboot', 'Start containers', 'Check all services'],
            self::UpdateAgent => ['Download agent binary', 'Check signature', 'Keep previous binary for rollback', 'Restart agent'],
            self::PruneBackups => ['Remove backups of deleted websites', 'Remove unused data'],
            self::SetSftp => ['Set SFTP access'],
            self::ConfigureHost => ['Save settings', 'Restart websites', 'Restart services'],
            self::SetPanelCertificate => ['Change certificate method', 'Get TLS certificate'],
        };
    }

    /** The actions that use the backup repository. The panel adds its credentials when it sends the job. */
    public function usesBackupRepository(): bool
    {
        return match ($this) {
            self::Backup, self::Restore, self::PruneBackups => true,
            default => false,
        };
    }
}
