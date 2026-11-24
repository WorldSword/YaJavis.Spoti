# Jarvis for Yandex Station - one-line install on Windows (PowerShell):
#   irm https://github.com/__REPO__/releases/latest/download/install.ps1 | iex
# Downloads, verifies and runs the installer. The installer uses Android Platform Tools.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$repo = if ($env:JARVIS_REPO) { $env:JARVIS_REPO } else { '__REPO__' }
if (-not [Environment]::Is64BitOperatingSystem) { Write-Error 'Jarvis installer needs 64-bit Windows'; return }
$name = 'JarvisSetup-windows-amd64.exe'
$base = "https://github.com/$repo/releases/latest/download"
$dir = Join-Path $env:TEMP ('JarvisSetup-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir $name
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
Write-Host "Downloading Jarvis installer from $repo ..."
Invoke-WebRequest -UseBasicParsing -Uri "$base/$name" -OutFile $exe
$sumsFile = Join-Path $dir 'SHA256SUMS'
Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile $sumsFile
$pattern = '^[a-fA-F0-9]{64}\s+\*?' + [regex]::Escape($name) + '\s*$'
$lines = @(Get-Content $sumsFile | Where-Object { $_ -match $pattern })
if ($lines.Count -ne 1) { throw 'Missing or ambiguous installer checksum' }
$want = ($lines[0] -split '\s+')[0]
if ($want -ine (Get-FileHash -Algorithm SHA256 $exe).Hash) { throw 'Installer checksum mismatch' }
Write-Host 'Starting the installer - a page will open in your browser.'
Start-Process -FilePath $exe
