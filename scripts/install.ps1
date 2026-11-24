# Run with: irm https://runners.retardsoftware.com/downloads/runnerctl/install.ps1 | iex
$ErrorActionPreference = 'Stop'

function Fail([string]$Message) { throw "runnerctl installer: $Message" }

function Assert-PrivatePath([string]$Path) {
    $acl = Get-Acl -LiteralPath $Path
    $userSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $trusted = @($userSid, 'S-1-5-18', 'S-1-5-32-544', 'S-1-3-0') # current user, SYSTEM, Administrators, CREATOR OWNER
    $owner = $acl.GetOwner([System.Security.Principal.SecurityIdentifier]).Value
    if ($owner -notin $trusted) { Fail 'installation path has an untrusted owner' }
    $descriptor = [System.Security.AccessControl.RawSecurityDescriptor]::new($acl.GetSecurityDescriptorBinaryForm(), 0)
    if ($null -eq $descriptor.DiscretionaryAcl) { Fail 'installation path has an unrestricted ACL' }
    $writeMask = [int64][System.Security.AccessControl.FileSystemRights]::Write -bor
                 [int64][System.Security.AccessControl.FileSystemRights]::Delete -bor
                 [int64][System.Security.AccessControl.FileSystemRights]::DeleteSubdirectoriesAndFiles -bor
                 [int64][System.Security.AccessControl.FileSystemRights]::ChangePermissions -bor
                 [int64][System.Security.AccessControl.FileSystemRights]::TakeOwnership
    foreach ($rule in $acl.GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier])) {
        if ($rule.AccessControlType -ne [System.Security.AccessControl.AccessControlType]::Allow) { continue }
        if (([int64]$rule.FileSystemRights -band $writeMask) -ne 0 -and $rule.IdentityReference.Value -notin $trusted) {
            Fail 'installation path is writable by other users'
        }
    }
}

$origin = 'https://runners.retardsoftware.com'
if ($env:RUNNERCTL_INSTALL_TEST_MODE -eq '1') {
    if ($env:RUNNERCTL_INSTALL_TEST_BASE_URL -notmatch '^http://(?:127\.0\.0\.1|localhost):[0-9]{1,5}$') {
        Fail 'test origin must be loopback HTTP'
    }
    $origin = $env:RUNNERCTL_INSTALL_TEST_BASE_URL
} elseif ($env:RUNNERCTL_INSTALL_TEST_BASE_URL) {
    Fail 'test origin requires RUNNERCTL_INSTALL_TEST_MODE=1'
}

if ($env:RUNNERCTL_INSTALL_TEST_MODE -eq '1' -and $env:RUNNERCTL_INSTALL_TEST_ARCH) {
    $arch = $env:RUNNERCTL_INSTALL_TEST_ARCH
} else {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
}
if ($arch -notin @('x64', 'arm64')) { Fail "unsupported architecture: $arch" }
if ($arch -eq 'x64') { $arch = 'amd64' }
if ($env:RUNNERCTL_INSTALL_TEST_MODE -ne '1' -and -not [System.Runtime.InteropServices.RuntimeInformation]::IsOSPlatform([System.Runtime.InteropServices.OSPlatform]::Windows)) {
    Fail 'native Windows is required'
}

function Download([string]$RelativePath, [string]$Destination, [long]$Limit) {
    $request = [System.Net.HttpWebRequest][System.Net.WebRequest]::Create($origin + $RelativePath)
    $request.AllowAutoRedirect = $false
    $request.Timeout = 15000
    $request.ReadWriteTimeout = 120000
    $response = $null
    $source = $null
    $file = $null
    try {
        $response = [System.Net.HttpWebResponse]$request.GetResponse()
        if ($response.StatusCode -ne [System.Net.HttpStatusCode]::OK) { Fail "download returned HTTP $([int]$response.StatusCode)" }
        if ($response.ContentLength -gt $Limit) { Fail 'download exceeds size limit' }
        $source = $response.GetResponseStream()
        $file = [System.IO.File]::Open($Destination, [System.IO.FileMode]::CreateNew)
        $buffer = New-Object byte[] 65536
        $count = [long]0
        while (($read = $source.Read($buffer, 0, $buffer.Length)) -gt 0) {
            $count += $read
            if ($count -gt $Limit) { Fail 'download exceeds size limit' }
            $file.Write($buffer, 0, $read)
        }
    } finally {
        if ($file) { $file.Dispose() }
        if ($source) { $source.Dispose() }
        if ($response) { $response.Dispose() }
    }
}

$scratch = Join-Path ([System.IO.Path]::GetTempPath()) ('runnerctl-install-' + [guid]::NewGuid().ToString('N'))
[System.IO.Directory]::CreateDirectory($scratch) | Out-Null
try {
    $versionFile = Join-Path $scratch 'VERSION'
    Download '/downloads/runnerctl/latest/VERSION' $versionFile 128
    $versionBytes = [System.IO.File]::ReadAllBytes($versionFile)
    $versionText = [System.Text.Encoding]::ASCII.GetString($versionBytes)
    if ($versionText -cnotmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\n$') { Fail 'invalid release version' }
    $version = $versionText.TrimEnd("`n")
    $archiveName = "runnerctl_${version}_windows_${arch}.zip"
    $base = "/downloads/runnerctl/$version"
    $checksums = Join-Path $scratch 'SHA256SUMS'
    $archive = Join-Path $scratch $archiveName
    Download "$base/SHA256SUMS" $checksums 16384
    Download "$base/$archiveName" $archive 67108864
    $matching = @([System.IO.File]::ReadAllLines($checksums) | Where-Object { $_ -match ('^[0-9a-f]{64}  (?:\./)?' + [regex]::Escape($archiveName) + '$') })
    if ($matching.Count -ne 1) { Fail 'invalid checksum entry' }
    $expected = $matching[0].Substring(0, 64)
    $actual = (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -cne $expected) { Fail 'archive checksum mismatch' }

    Add-Type -AssemblyName System.IO.Compression
    $archiveStream = [System.IO.File]::OpenRead($archive)
    try {
        $zip = New-Object System.IO.Compression.ZipArchive($archiveStream, [System.IO.Compression.ZipArchiveMode]::Read)
        try {
            $allowed = @('LICENSE', 'README.md', 'SECURITY.md', 'VERSION', 'runnerctl.exe')
            if ($zip.Entries.Count -ne $allowed.Count) { Fail 'unsafe archive members' }
            $seen = @{}
            $binaryEntry = $null
            foreach ($entry in $zip.Entries) {
                if ($entry.FullName -cnotin $allowed -or $seen.ContainsKey($entry.FullName)) { Fail 'unsafe archive members' }
                $seen[$entry.FullName] = $true
                $type = ($entry.ExternalAttributes -shr 16) -band 0xF000
                if ($type -ne 0 -and $type -ne 0x8000) { Fail 'archive contains a link or special file' }
                if ($entry.FullName -ceq 'runnerctl.exe') { $binaryEntry = $entry }
            }
            if (-not $binaryEntry -or $binaryEntry.Length -gt 67108864) { Fail 'invalid binary entry' }

            if (-not $env:LOCALAPPDATA) { Fail 'LOCALAPPDATA is required' }
            $programs = Join-Path $env:LOCALAPPDATA 'Programs'
            $installDir = Join-Path $programs 'runnerctl'
            foreach ($path in @($env:LOCALAPPDATA, $programs, $installDir)) {
                $item = Get-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue
                if ($item) {
                    if ($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) { Fail 'installation path contains a reparse point' }
                    if (-not $item.PSIsContainer) { Fail 'installation path is not a directory' }
                }
            }
            [System.IO.Directory]::CreateDirectory($installDir) | Out-Null
            foreach ($path in @($env:LOCALAPPDATA, $programs, $installDir)) {
                Assert-PrivatePath $path
            }
            $target = Join-Path $installDir 'runnerctl.exe'
            $item = Get-Item -LiteralPath $target -Force -ErrorAction SilentlyContinue
            if ($item) {
                if (($item.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -or $item.PSIsContainer) { Fail 'existing runnerctl is not a regular file' }
                Assert-PrivatePath $target
            }
            $staged = Join-Path $installDir ('.runnerctl-' + [guid]::NewGuid().ToString('N') + '.exe')
            try {
                $input = $null
                $output = $null
                try {
                    $input = $binaryEntry.Open()
                    $output = [System.IO.File]::Open($staged, [System.IO.FileMode]::CreateNew)
                    $buffer = New-Object byte[] 65536
                    $count = [long]0
                    while (($read = $input.Read($buffer, 0, $buffer.Length)) -gt 0) {
                        $count += $read
                        if ($count -gt 67108864) { Fail 'binary exceeds size limit' }
                        $output.Write($buffer, 0, $read)
                    }
                } finally {
                    if ($output) { $output.Dispose() }
                    if ($input) { $input.Dispose() }
                }
                if ([System.IO.File]::Exists($target)) {
                    $backup = Join-Path $installDir ('.runnerctl-backup-' + [guid]::NewGuid().ToString('N'))
                    try { [System.IO.File]::Replace($staged, $target, $backup) }
                    finally { if ([System.IO.File]::Exists($backup)) { [System.IO.File]::Delete($backup) } }
                } else {
                    [System.IO.File]::Move($staged, $target)
                }
            } finally { if ([System.IO.File]::Exists($staged)) { [System.IO.File]::Delete($staged) } }
            Write-Output "Installed runnerctl $version at $target"
            if (($env:Path -split ';') -notcontains $installDir) {
                $env:Path = ($env:Path.TrimEnd(';') + ';' + $installDir)
            }
            if ($env:RUNNERCTL_INSTALL_TEST_MODE -ne '1') {
                $userPath = [System.Environment]::GetEnvironmentVariable('Path', [System.EnvironmentVariableTarget]::User)
                if (-not $userPath) { $userPath = '' }
                if (($userPath -split ';') -notcontains $installDir) {
                    $newUserPath = (($userPath.TrimEnd(';') + ';' + $installDir).TrimStart(';'))
                    try {
                        [System.Environment]::SetEnvironmentVariable('Path', $newUserPath, [System.EnvironmentVariableTarget]::User)
                        Write-Output "Added $installDir to your user Path for future terminals."
                    } catch {
                        Write-Warning "Could not update user Path; add $installDir in Windows Settings."
                    }
                }
            }
        } finally { if ($zip) { $zip.Dispose() } }
    } finally { $archiveStream.Dispose() }
} finally {
    [System.IO.Directory]::Delete($scratch, $true)
}
