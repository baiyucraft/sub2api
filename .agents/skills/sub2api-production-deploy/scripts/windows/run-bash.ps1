[CmdletBinding(PositionalBinding = $false)]
param(
    [Parameter(Mandatory = $true, Position = 0)]
    [ValidateSet('syntax', 'test', 'command', 'capabilities')]
    [string]$Mode,

    [Parameter(Position = 1, ValueFromRemainingArguments = $true)]
    [string[]]$RemainingArgs
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$ExitUsage = 64
$ExitUnavailable = 69
$ExitVmRequired = 78
$LocalCommands = @(
    'awk',
    'sed',
    'grep',
    'find',
    'sha256sum',
    'openssl',
    'realpath',
    'stat',
    'timeout'
)
$VmOnlyCommands = @('jq', 'docker', 'systemctl', 'flock')
$script:LastBashExitCode = 0

# Bash reads BASH_ENV for non-interactive shells independently of profile
# loading flags. Remove both startup hooks for every child process launched by
# this wrapper. Environment changes are scoped to this PowerShell process.
[Environment]::SetEnvironmentVariable('BASH_ENV', $null)
[Environment]::SetEnvironmentVariable('ENV', $null)

function Exit-WithMessage {
    param(
        [Parameter(Mandatory = $true)]
        [int]$Code,

        [Parameter(Mandatory = $true)]
        [string]$Message
    )

    [Console]::Error.WriteLine($Message)
    exit $Code
}

function Resolve-BashExecutable {
    $override = [Environment]::GetEnvironmentVariable('SUB2API_BASH_EXE')
    if (-not [string]::IsNullOrWhiteSpace($override)) {
        if (-not (Test-Path -LiteralPath $override -PathType Leaf)) {
            Exit-WithMessage -Code $ExitUnavailable -Message 'bash_status=unavailable source=SUB2API_BASH_EXE reason=not_found'
        }
        return (Resolve-Path -LiteralPath $override).Path
    }

    $pathCommand = Get-Command -Name 'bash' -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $pathCommand -and (Test-Path -LiteralPath $pathCommand.Source -PathType Leaf)) {
        return (Resolve-Path -LiteralPath $pathCommand.Source).Path
    }

    foreach ($candidate in @(
        'C:\Program Files\Git\bin\bash.exe',
        'C:\Program Files\Git\usr\bin\bash.exe'
    )) {
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }
    }

    Exit-WithMessage -Code $ExitUnavailable -Message 'bash_status=unavailable reason=git_bash_not_found'
}

function Invoke-BashRaw {
    param(
        [Parameter(Mandatory = $true)]
        [string]$BashExecutable,

        [Parameter(Mandatory = $true)]
        [string[]]$ArgumentList
    )

    & $BashExecutable '--noprofile' '--norc' @ArgumentList
    $script:LastBashExitCode = $LASTEXITCODE
}

function Convert-ToBashPath {
    param(
        [Parameter(Mandatory = $true)]
        [string]$BashExecutable,

        [Parameter(Mandatory = $true)]
        [string]$WindowsPath
    )

    $resolved = [System.IO.Path]::GetFullPath($WindowsPath)
    $converted = @(
        & $BashExecutable '--noprofile' '--norc' '-c' 'cygpath -u -- "$1"' 'sub2api-run-bash' $resolved
    )
    $code = $LASTEXITCODE
    if ($code -ne 0) {
        Exit-WithMessage -Code $ExitUnavailable -Message 'bash_status=unavailable reason=cygpath_failed'
    }

    $path = $converted | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -Last 1
    if ([string]::IsNullOrWhiteSpace($path)) {
        Exit-WithMessage -Code $ExitUnavailable -Message 'bash_status=unavailable reason=cygpath_empty'
    }
    return $path
}

function Convert-WorkspaceArgument {
    param(
        [Parameter(Mandatory = $true)]
        [string]$BashExecutable,

        [AllowEmptyString()]
        [string]$Value
    )

    if ([string]::IsNullOrEmpty($Value) -or -not [System.IO.Path]::IsPathRooted($Value)) {
        return $Value
    }

    $workspace = [System.IO.Path]::GetFullPath((Get-Location).ProviderPath).TrimEnd('\', '/')
    $candidate = [System.IO.Path]::GetFullPath($Value)
    $isWorkspacePath = $candidate.Equals($workspace, [System.StringComparison]::OrdinalIgnoreCase) -or
        $candidate.StartsWith($workspace + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)
    if (-not $isWorkspacePath) {
        return $Value
    }

    return Convert-ToBashPath -BashExecutable $BashExecutable -WindowsPath $candidate
}

function Test-BashCommand {
    param(
        [Parameter(Mandatory = $true)]
        [string]$BashExecutable,

        [Parameter(Mandatory = $true)]
        [string]$CommandName
    )

    & $BashExecutable '--noprofile' '--norc' '-c' 'command -v -- "$1" >/dev/null 2>&1' 'sub2api-run-bash' $CommandName
    return $LASTEXITCODE -eq 0
}

$bash = Resolve-BashExecutable
[string[]]$arguments = @()
if ($null -ne $RemainingArgs) {
    $arguments = @($RemainingArgs)
}

switch ($Mode) {
    'syntax' {
        if ($arguments.Count -ne 1) {
            Exit-WithMessage -Code $ExitUsage -Message 'usage: run-bash.ps1 syntax <script>'
        }
        if (-not (Test-Path -LiteralPath $arguments[0] -PathType Leaf)) {
            Exit-WithMessage -Code $ExitUsage -Message 'script_status=invalid reason=not_found'
        }

        $scriptPath = Convert-ToBashPath -BashExecutable $bash -WindowsPath (Resolve-Path -LiteralPath $arguments[0]).Path
        Invoke-BashRaw -BashExecutable $bash -ArgumentList @('-n', $scriptPath)
        exit $script:LastBashExitCode
    }

    'test' {
        if ($arguments.Count -lt 1) {
            Exit-WithMessage -Code $ExitUsage -Message 'usage: run-bash.ps1 test <script> [arguments]'
        }
        if (-not (Test-Path -LiteralPath $arguments[0] -PathType Leaf)) {
            Exit-WithMessage -Code $ExitUsage -Message 'script_status=invalid reason=not_found'
        }

        $scriptPath = Convert-ToBashPath -BashExecutable $bash -WindowsPath (Resolve-Path -LiteralPath $arguments[0]).Path
        $scriptArguments = @()
        if ($arguments.Count -gt 1) {
            foreach ($argument in $arguments[1..($arguments.Count - 1)]) {
                $scriptArguments += Convert-WorkspaceArgument -BashExecutable $bash -Value $argument
            }
        }
        $invokeArguments = @($scriptPath) + $scriptArguments
        Invoke-BashRaw -BashExecutable $bash -ArgumentList $invokeArguments
        exit $script:LastBashExitCode
    }

    'command' {
        if ($arguments.Count -lt 1) {
            Exit-WithMessage -Code $ExitUsage -Message 'usage: run-bash.ps1 command <allowlisted-command> [arguments]'
        }

        $commandName = $arguments[0]
        if ($VmOnlyCommands -contains $commandName) {
            Exit-WithMessage -Code $ExitVmRequired -Message "command_status=vm_required command=$commandName"
        }
        if ($LocalCommands -notcontains $commandName) {
            Exit-WithMessage -Code $ExitUsage -Message "command_status=rejected command=$commandName reason=not_allowlisted"
        }
        if (-not (Test-BashCommand -BashExecutable $bash -CommandName $commandName)) {
            Exit-WithMessage -Code $ExitUnavailable -Message "command_status=unavailable command=$commandName"
        }

        $commandArguments = @()
        if ($arguments.Count -gt 1) {
            foreach ($argument in $arguments[1..($arguments.Count - 1)]) {
                $commandArguments += Convert-WorkspaceArgument -BashExecutable $bash -Value $argument
            }
        }
        $invokeArguments = @('-c', 'exec "$@"', 'sub2api-run-bash', $commandName) + $commandArguments
        Invoke-BashRaw -BashExecutable $bash -ArgumentList $invokeArguments
        exit $script:LastBashExitCode
    }

    'capabilities' {
        if ($arguments.Count -ne 0) {
            Exit-WithMessage -Code $ExitUsage -Message 'usage: run-bash.ps1 capabilities'
        }

        $commandStatuses = [ordered]@{}
        foreach ($commandName in $LocalCommands) {
            $commandStatuses[$commandName] = if (Test-BashCommand -BashExecutable $bash -CommandName $commandName) { 'available' } else { 'unavailable' }
        }
        foreach ($commandName in $VmOnlyCommands) {
            $commandStatuses[$commandName] = 'vm_required'
        }

        $versionOutput = @(& $bash '--noprofile' '--norc' '-c' 'printf "%s" "$BASH_VERSION"')
        $version = if ($LASTEXITCODE -eq 0) { [string]::Join('', $versionOutput) } else { 'unknown' }
        [ordered]@{
            schema = 'sub2api.windows-shell-capabilities/v1'
            bash_path = $bash
            bash_version = $version
            commands = $commandStatuses
        } | ConvertTo-Json -Depth 4 -Compress
        exit 0
    }
}
